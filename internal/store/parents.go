package store

import (
	"errors"
	"sort"
	"strings"
)

// Parents are linked to their scholars. Anyone who isn't a scholar can be a
// parent: people with the Parent role, and mentors, board members or admins
// with children in the school. A parent sees their scholars' classes,
// assignments and work, and can turn work in for them.

// ClassParent is the role a parent has in their scholars' classes. It isn't
// stored in class_members; ClassRelation works it out from the links.
const ClassParent = "parent"

// ErrNotScholar means a parent link was asked for between the wrong kinds of
// account: the child must be a scholar, and the parent must not be.
var ErrNotScholar = errors.New("only scholars have parents")

// LinkParent makes parentID a parent of scholarID. It reports whether the
// link is new.
func (s *Store) LinkParent(parentID, scholarID int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(`SELECT (SELECT role FROM users WHERE id = ?) = 'scholar'
		AND (SELECT role FROM users WHERE id = ?) <> 'scholar'`, scholarID, parentID).Scan(&ok)
	if err != nil {
		return false, err
	}
	if !ok || parentID == scholarID {
		return false, ErrNotScholar
	}
	res, err := s.db.Exec(`INSERT INTO parents (parent_id, scholar_id, added_at) VALUES (?, ?, ?)
		ON CONFLICT (parent_id, scholar_id) DO NOTHING`, parentID, scholarID, s.now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// UnlinkParent removes a parent link.
func (s *Store) UnlinkParent(parentID, scholarID int64) error {
	_, err := s.db.Exec(`DELETE FROM parents WHERE parent_id = ? AND scholar_id = ?`, parentID, scholarID)
	return err
}

// IsParentOf reports whether parentID is a parent of scholarID.
func (s *Store) IsParentOf(parentID, scholarID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM parents WHERE parent_id = ? AND scholar_id = ?`, parentID, scholarID).Scan(&n)
	return n > 0, err
}

// ScholarsOf lists a parent's scholars by name, including deactivated ones.
func (s *Store) ScholarsOf(parentID int64) ([]*User, error) {
	return s.queryUsers(`SELECT `+userColsAs("u")+` FROM parents p JOIN users u ON u.id = p.scholar_id
		WHERE p.parent_id = ? ORDER BY u.display_name COLLATE NOCASE`, parentID)
}

// ParentsOf lists a scholar's parents by name, including deactivated ones.
func (s *Store) ParentsOf(scholarID int64) ([]*User, error) {
	return s.queryUsers(`SELECT `+userColsAs("u")+` FROM parents p JOIN users u ON u.id = p.parent_id
		WHERE p.scholar_id = ? ORDER BY u.display_name COLLATE NOCASE`, scholarID)
}

// ScholarsInClass lists a parent's active scholars who are in a class.
func (s *Store) ScholarsInClass(parentID, classID int64) ([]*User, error) {
	return s.queryUsers(`SELECT `+userColsAs("u")+` FROM parents p JOIN users u ON u.id = p.scholar_id
		JOIN class_members m ON m.user_id = u.id AND m.class_id = ? AND m.role = 'scholar'
		WHERE p.parent_id = ? AND u.active = 1 ORDER BY u.display_name COLLATE NOCASE`, classID, parentID)
}

func (s *Store) queryUsers(q string, args ...any) ([]*User, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ClassRelation is a user's role in a class: ClassMentor or ClassScholar if
// they're in it, ClassParent if one of their active scholars is, or "".
func (s *Store) ClassRelation(classID, userID int64) (string, error) {
	role, err := s.ClassRole(classID, userID)
	if role != "" || err != nil {
		return role, err
	}
	var n int
	err = s.db.QueryRow(`SELECT COUNT(*) FROM parents p JOIN class_members m ON m.user_id = p.scholar_id
		JOIN users u ON u.id = p.scholar_id
		WHERE p.parent_id = ? AND m.class_id = ? AND m.role = 'scholar' AND u.active = 1`, userID, classID).Scan(&n)
	if n > 0 {
		return ClassParent, err
	}
	return "", err
}

// parentClasses lists the current classes of a parent's active scholars that
// the parent isn't in themselves, with MyRole ClassParent and Children set.
func (s *Store) parentClasses(parentID int64) ([]*Class, error) {
	rows, err := s.db.Query(`SELECT `+classCols+`, u.display_name FROM classes c
		JOIN class_members m ON m.class_id = c.id AND m.role = 'scholar'
		JOIN parents p ON p.scholar_id = m.user_id AND p.parent_id = ?
		JOIN users u ON u.id = m.user_id AND u.active = 1
		WHERE c.archived = 0 AND c.id NOT IN (SELECT class_id FROM class_members WHERE user_id = ?)
		ORDER BY u.display_name COLLATE NOCASE`, parentID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]*Class{}
	var out []*Class
	for rows.Next() {
		var child string
		c, err := scanClass(rows, &child)
		if err != nil {
			return nil, err
		}
		if have := byID[c.ID]; have != nil {
			have.Children = append(have.Children, child)
			continue
		}
		c.MyRole, c.Children = ClassParent, []string{child}
		byID[c.ID] = c
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChildNames joins the names of the viewer's scholars in a class, for parents.
func (c *Class) ChildNames() string { return strings.Join(c.Children, ", ") }

// sortClasses orders classes by name, as the class lists show them.
func sortClasses(list []*Class) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if a != b {
			return a < b
		}
		return list[i].ID < list[j].ID
	})
}
