package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Roles within a class.
const (
	ClassMentor  = "mentor"
	ClassScholar = "scholar"
)

// ClassColors are the colors a class can have, in the order they're offered.
var ClassColors = []string{"blue", "teal", "green", "gold", "orange", "red", "purple", "gray"}

// ValidColor reports whether c is one of ClassColors.
func ValidColor(c string) bool {
	for _, x := range ClassColors {
		if x == c {
			return true
		}
	}
	return false
}

// Class is a class, with counts filled in by the list queries.
type Class struct {
	ID          int64
	Name        string
	Description string
	Term        string
	Meets       string
	Color       string
	Archived    bool
	CreatedAt   int64
	UpdatedAt   int64

	Mentors  []*User // filled in by ClassMentors/ListClasses
	Scholars int     // number of active scholars
	MyRole   string  // the viewer's role in the class, from ListClassesFor
}

// MentorNames joins the mentors' names for display.
func (c *Class) MentorNames() string {
	names := make([]string, len(c.Mentors))
	for i, m := range c.Mentors {
		names[i] = m.DisplayName
	}
	return strings.Join(names, ", ")
}

const classCols = `c.id, c.name, c.description, c.term, c.meets, c.color, c.archived, c.created_at, c.updated_at`

func scanClass(row interface{ Scan(...any) error }, extra ...any) (*Class, error) {
	c := &Class{}
	dst := append([]any{&c.ID, &c.Name, &c.Description, &c.Term, &c.Meets, &c.Color, &c.Archived, &c.CreatedAt, &c.UpdatedAt}, extra...)
	if err := row.Scan(dst...); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	return c, nil
}

// CreateClass adds a class and sets c.ID.
func (s *Store) CreateClass(c *Class) error {
	now := s.now()
	c.CreatedAt, c.UpdatedAt = now, now
	res, err := s.db.Exec(`INSERT INTO classes (name, description, term, meets, color, archived, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, c.Name, c.Description, c.Term, c.Meets, c.Color, c.Archived, now, now)
	if err != nil {
		return err
	}
	c.ID, err = res.LastInsertId()
	return err
}

// UpdateClass saves a class's details, including whether it's archived.
func (s *Store) UpdateClass(c *Class) error {
	c.UpdatedAt = s.now()
	res, err := s.db.Exec(`UPDATE classes SET name = ?, description = ?, term = ?, meets = ?, color = ?, archived = ?,
		updated_at = ? WHERE id = ?`, c.Name, c.Description, c.Term, c.Meets, c.Color, c.Archived, c.UpdatedAt, c.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteClass removes a class and its memberships.
func (s *Store) DeleteClass(id int64) error {
	_, err := s.db.Exec(`DELETE FROM classes WHERE id = ?`, id)
	return err
}

// GetClass returns a class with its mentors and scholar count.
func (s *Store) GetClass(id int64) (*Class, error) {
	c, err := scanClass(s.db.QueryRow(`SELECT `+classCols+` FROM classes c WHERE c.id = ?`, id))
	if err != nil {
		return nil, err
	}
	return c, s.fillClasses([]*Class{c})
}

// ClassFilter narrows ListClasses.
type ClassFilter struct {
	Term     string // "" for every term
	Archived bool   // archived classes instead of current ones
	Query    string // matches the name
}

// ListClasses returns classes sorted by name, with mentors and counts.
func (s *Store) ListClasses(f ClassFilter) ([]*Class, error) {
	q := `SELECT ` + classCols + ` FROM classes c WHERE c.archived = ?`
	args := []any{f.Archived}
	if f.Term != "" {
		q += ` AND c.term = ?`
		args = append(args, f.Term)
	}
	if t := strings.TrimSpace(f.Query); t != "" {
		q += ` AND c.name LIKE ? ESCAPE '\'`
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(t)+"%")
	}
	q += ` ORDER BY c.name COLLATE NOCASE, c.id`
	return s.queryClasses(q, args...)
}

// ListClassesFor returns the current (not archived) classes a user is in,
// with their role in each.
func (s *Store) ListClassesFor(userID int64) ([]*Class, error) {
	rows, err := s.db.Query(`SELECT `+classCols+`, m.role FROM classes c JOIN class_members m ON m.class_id = c.id
		WHERE m.user_id = ? AND c.archived = 0 ORDER BY c.name COLLATE NOCASE, c.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Class
	for rows.Next() {
		var role string
		c, err := scanClass(rows, &role)
		if err != nil {
			return nil, err
		}
		c.MyRole = role
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.fillClasses(out)
}

func (s *Store) queryClasses(q string, args ...any) ([]*Class, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Class
	for rows.Next() {
		c, err := scanClass(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.fillClasses(out)
}

// fillClasses adds mentors and scholar counts.
func (s *Store) fillClasses(list []*Class) error {
	if len(list) == 0 {
		return nil
	}
	byID := map[int64]*Class{}
	ids := make([]string, len(list))
	args := make([]any, len(list))
	for i, c := range list {
		byID[c.ID] = c
		ids[i] = "?"
		args[i] = c.ID
	}
	in := strings.Join(ids, ",")
	rows, err := s.db.Query(`SELECT m.class_id, `+userColsAs("u")+` FROM class_members m JOIN users u ON u.id = m.user_id
		WHERE m.role = 'mentor' AND u.active = 1 AND m.class_id IN (`+in+`) ORDER BY u.display_name COLLATE NOCASE`, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int64
		u := &User{}
		if err := rows.Scan(append([]any{&cid}, userDest(u)...)...); err != nil {
			rows.Close()
			return err
		}
		byID[cid].Mentors = append(byID[cid].Mentors, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.db.Query(`SELECT m.class_id, COUNT(*) FROM class_members m JOIN users u ON u.id = m.user_id
		WHERE m.role = 'scholar' AND u.active = 1 AND m.class_id IN (`+in+`) GROUP BY m.class_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int64
		var n int
		if err := rows.Scan(&cid, &n); err != nil {
			return err
		}
		byID[cid].Scholars = n
	}
	return rows.Err()
}

// Terms lists the terms classes use, newest first by when they were last used.
func (s *Store) Terms() ([]string, error) {
	rows, err := s.db.Query(`SELECT term FROM classes WHERE term <> '' GROUP BY term ORDER BY MAX(created_at) DESC, term`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountClasses returns the number of current classes.
func (s *Store) CountClasses() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM classes WHERE archived = 0`).Scan(&n)
	return n, err
}

// ------------------------------------------------------------- members

// Member is someone in a class.
type Member struct {
	*User
	ClassRole string
	AddedAt   int64
}

// ClassMembers lists a class's members: mentors first, then scholars, by
// name. Deactivated people are included only if withInactive.
func (s *Store) ClassMembers(classID int64, withInactive bool) ([]*Member, error) {
	q := `SELECT m.role, m.added_at, ` + userColsAs("u") + ` FROM class_members m JOIN users u ON u.id = m.user_id
		WHERE m.class_id = ?`
	if !withInactive {
		q += ` AND u.active = 1`
	}
	q += ` ORDER BY m.role = 'scholar', u.display_name COLLATE NOCASE`
	rows, err := s.db.Query(q, classID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Member
	for rows.Next() {
		m := &Member{User: &User{}}
		if err := rows.Scan(append([]any{&m.ClassRole, &m.AddedAt}, userDest(m.User)...)...); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ClassRole returns a user's role in a class, or "" if they're not in it.
func (s *Store) ClassRole(classID, userID int64) (string, error) {
	var role string
	err := s.db.QueryRow(`SELECT role FROM class_members WHERE class_id = ? AND user_id = ?`, classID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return role, err
}

// AddMembers puts users in a class with a role (changing it if they're
// already in). It returns how many were added or changed.
func (s *Store) AddMembers(classID int64, role string, userIDs []int64) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, id := range userIDs {
		res, err := tx.Exec(`INSERT INTO class_members (class_id, user_id, role, added_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (class_id, user_id) DO UPDATE SET role = excluded.role WHERE role <> excluded.role`,
			classID, id, role, s.now())
		if err != nil {
			return 0, err
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	return n, tx.Commit()
}

// RemoveMember takes a user out of a class.
func (s *Store) RemoveMember(classID, userID int64) error {
	_, err := s.db.Exec(`DELETE FROM class_members WHERE class_id = ? AND user_id = ?`, classID, userID)
	return err
}
