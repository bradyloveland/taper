package store

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// Kinds of notification. Each person can turn any of them off.
const (
	NotifyAssignment = "assignment" // a new assignment in your class
	NotifyWork       = "work"       // work turned in (mentors); feedback, complete, returned (scholars, parents)
	NotifyDue        = "due"        // work due tomorrow and not turned in
	NotifyChat       = "chat"       // new messages
	NotifyCalendar   = "calendar"   // new, changed or cancelled events
)

// NotifyKinds lists every kind, in the order the settings show them.
var NotifyKinds = []string{NotifyAssignment, NotifyWork, NotifyDue, NotifyChat, NotifyCalendar}

// Email choices.
const (
	EmailOff   = "off"
	EmailEach  = "each"
	EmailDaily = "daily"
)

// Notification is something to tell one person about.
type Notification struct {
	ID        int64
	UserID    int64
	Kind      string
	Ref       string
	Title     string
	Link      string
	Count     int
	CreatedAt int64
	UpdatedAt int64
	ReadAt    int64 // 0 if unread
	EmailedAt int64 // 0 while waiting to be emailed
}

// Unread reports whether the person hasn't opened it yet.
func (n *Notification) Unread() bool { return n.ReadAt == 0 }

// Notice is a notification to send to several people. Unread notifications
// with the same Ref are combined into one: Many, with {n} for the count, is
// its title then (or Title, if Many is empty).
type Notice struct {
	Kind  string
	Ref   string
	Title string
	Many  string
	Link  string
}

const notificationCols = `id, user_id, kind, ref, title, link, count, created_at, updated_at, COALESCE(read_at, 0),
	COALESCE(emailed_at, 0)`

func scanNotification(row interface{ Scan(...any) error }) (*Notification, error) {
	n := &Notification{}
	err := row.Scan(&n.ID, &n.UserID, &n.Kind, &n.Ref, &n.Title, &n.Link, &n.Count, &n.CreatedAt, &n.UpdatedAt, &n.ReadAt, &n.EmailedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

// NotifyPrefs are one person's notification choices.
type NotifyPrefs struct {
	UserID   int64
	Email    string          // EmailOff, EmailEach or EmailDaily
	Off      map[string]bool // kinds turned off
	DigestAt int64           // when the last daily summary went
}

// Wants reports whether the person wants notifications of this kind.
func (p *NotifyPrefs) Wants(kind string) bool { return !p.Off[kind] }

func parseOff(s string) map[string]bool {
	off := map[string]bool{}
	for _, k := range strings.Split(s, ",") {
		if k != "" {
			off[k] = true
		}
	}
	return off
}

// GetNotifyPrefs returns a person's choices, or the defaults: everything on,
// no email.
func (s *Store) GetNotifyPrefs(userID int64) (*NotifyPrefs, error) {
	p := &NotifyPrefs{UserID: userID, Email: EmailOff}
	var off string
	err := s.db.QueryRow(`SELECT email, off_kinds, digest_at FROM notify_prefs WHERE user_id = ?`, userID).Scan(&p.Email, &off, &p.DigestAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	p.Off = parseOff(off)
	return p, nil
}

// SetNotifyPrefs saves a person's choices (not DigestAt). Turning email off
// means nothing waiting is sent later.
func (s *Store) SetNotifyPrefs(p *NotifyPrefs) error {
	var off []string
	for _, k := range NotifyKinds {
		if p.Off[k] {
			off = append(off, k)
		}
	}
	if p.Email != EmailEach && p.Email != EmailDaily {
		p.Email = EmailOff
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO notify_prefs (user_id, email, off_kinds) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET email = excluded.email, off_kinds = excluded.off_kinds`,
		p.UserID, p.Email, strings.Join(off, ",")); err != nil {
		return err
	}
	if p.Email != EmailDaily {
		if _, err := tx.Exec(`UPDATE notifications SET emailed_at = ? WHERE user_id = ? AND emailed_at IS NULL`,
			s.now(), p.UserID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// prefsFor returns the choices of several people; those with none get the
// defaults.
func prefsFor(tx *sql.Tx, ids []int64) (map[int64]*NotifyPrefs, error) {
	out := make(map[int64]*NotifyPrefs, len(ids))
	for _, id := range ids {
		out[id] = &NotifyPrefs{UserID: id, Email: EmailOff, Off: map[string]bool{}}
	}
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := tx.Query(`SELECT user_id, email, off_kinds, digest_at FROM notify_prefs WHERE user_id IN (`+marks+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			p := &NotifyPrefs{}
			var off string
			if err := rows.Scan(&p.UserID, &p.Email, &off, &p.DigestAt); err != nil {
				rows.Close()
				return nil, err
			}
			p.Off = parseOff(off)
			out[p.UserID] = p
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Notify tells each of the people about n, skipping those who turned its kind
// off, and combining it with an unread notification with the same Ref. It
// returns the new notifications that are to be emailed now (to people who
// chose an email for each one).
func (s *Store) Notify(n Notice, userIDs []int64) ([]*Notification, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	prefs, err := prefsFor(tx, userIDs)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var emailNow []*Notification
	for _, uid := range userIDs {
		p := prefs[uid]
		if !p.Wants(n.Kind) {
			continue
		}
		if n.Ref != "" {
			var id int64
			var count int
			err := tx.QueryRow(`SELECT id, count FROM notifications WHERE user_id = ? AND ref = ? AND read_at IS NULL
				ORDER BY id DESC LIMIT 1`, uid, n.Ref).Scan(&id, &count)
			if err == nil {
				title := n.Title
				if n.Many != "" {
					title = strings.Replace(n.Many, "{n}", strconv.Itoa(count+1), 1)
				}
				if _, err := tx.Exec(`UPDATE notifications SET count = count + 1, title = ?, link = ?, updated_at = ? WHERE id = ?`,
					title, n.Link, now, id); err != nil {
					return nil, err
				}
				continue
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, err
			}
		}
		var emailed any = now
		if p.Email != EmailOff {
			emailed = nil
		}
		res, err := tx.Exec(`INSERT INTO notifications (user_id, kind, ref, title, link, created_at, updated_at, emailed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, uid, n.Kind, n.Ref, n.Title, n.Link, now, now, emailed)
		if err != nil {
			return nil, err
		}
		if p.Email == EmailEach {
			id, _ := res.LastInsertId()
			emailNow = append(emailNow, &Notification{ID: id, UserID: uid, Kind: n.Kind, Ref: n.Ref, Title: n.Title, Link: n.Link,
				Count: 1, CreatedAt: now, UpdatedAt: now})
		}
	}
	return emailNow, tx.Commit()
}

// ListNotifications returns a person's latest notifications, newest first.
func (s *Store) ListNotifications(userID int64, limit int) ([]*Notification, error) {
	return s.queryNotifications(`SELECT `+notificationCols+` FROM notifications WHERE user_id = ?
		ORDER BY updated_at DESC, id DESC LIMIT ?`, userID, limit)
}

func (s *Store) queryNotifications(q string, args ...any) ([]*Notification, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNotification returns one of a person's notifications.
func (s *Store) GetNotification(userID, id int64) (*Notification, error) {
	return scanNotification(s.db.QueryRow(`SELECT `+notificationCols+` FROM notifications WHERE id = ? AND user_id = ?`, id, userID))
}

// UnreadNotifications counts a person's unread notifications.
func (s *Store) UnreadNotifications(userID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// MarkNotificationRead marks one notification read.
func (s *Store) MarkNotificationRead(userID, id int64) error {
	_, err := s.db.Exec(`UPDATE notifications SET read_at = ? WHERE id = ? AND user_id = ? AND read_at IS NULL`, s.now(), id, userID)
	return err
}

// MarkAllNotificationsRead marks all of a person's notifications read.
func (s *Store) MarkAllNotificationsRead(userID int64) error {
	_, err := s.db.Exec(`UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL`, s.now(), userID)
	return err
}

// MarkRefRead marks a person's unread notifications about ref read, like
// those for a chat once they've opened it.
func (s *Store) MarkRefRead(userID int64, ref string) error {
	_, err := s.db.Exec(`UPDATE notifications SET read_at = ? WHERE user_id = ? AND ref = ? AND read_at IS NULL`, s.now(), userID, ref)
	return err
}

// MarkEmailed records that notifications were emailed (or won't be).
func (s *Store) MarkEmailed(ids []int64) error {
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE notifications SET emailed_at = ? WHERE id = ? AND emailed_at IS NULL`, s.now(), id); err != nil {
			return err
		}
	}
	return nil
}

// DigestDue lists the people who chose a daily summary and haven't had one
// since the time given.
func (s *Store) DigestDue(since int64) ([]*NotifyPrefs, error) {
	rows, err := s.db.Query(`SELECT p.user_id, p.email, p.off_kinds, p.digest_at FROM notify_prefs p JOIN users u ON u.id = p.user_id
		WHERE p.email = 'daily' AND p.digest_at < ? AND u.active = 1`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*NotifyPrefs
	for rows.Next() {
		p := &NotifyPrefs{}
		var off string
		if err := rows.Scan(&p.UserID, &p.Email, &off, &p.DigestAt); err != nil {
			return nil, err
		}
		p.Off = parseOff(off)
		out = append(out, p)
	}
	return out, rows.Err()
}

// DigestFor returns a person's unread notifications not yet emailed, oldest
// first, and records that their summary went now.
func (s *Store) DigestFor(userID int64) ([]*Notification, error) {
	list, err := s.queryNotifications(`SELECT `+notificationCols+` FROM notifications
		WHERE user_id = ? AND emailed_at IS NULL AND read_at IS NULL ORDER BY updated_at, id`, userID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if _, err := s.db.Exec(`UPDATE notifications SET emailed_at = ? WHERE user_id = ? AND emailed_at IS NULL`, now, userID); err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`UPDATE notify_prefs SET digest_at = ? WHERE user_id = ?`, now, userID)
	return list, err
}

// Once reports whether key is new, recording it, so a reminder goes once.
func (s *Store) Once(key string) (bool, error) {
	res, err := s.db.Exec(`INSERT INTO notify_sent (key, created_at) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`, key, s.now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// PruneNotifications deletes notifications and reminder records older than
// before.
func (s *Store) PruneNotifications(before int64) error {
	if _, err := s.db.Exec(`DELETE FROM notifications WHERE updated_at < ?`, before); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM notify_sent WHERE created_at < ?`, before)
	return err
}

// --------------------------------------------------------------- audiences

// ActiveUserIDs lists everyone who can sign in.
func (s *Store) ActiveUserIDs() ([]int64, error) {
	return s.ids(`SELECT id FROM users WHERE active = 1`)
}

// ClassAudience lists a class's active members and the parents of its active
// scholars: everyone who follows the class.
func (s *Store) ClassAudience(classID int64) ([]int64, error) {
	return s.ids(`SELECT m.user_id FROM class_members m JOIN users u ON u.id = m.user_id WHERE m.class_id = ?1 AND u.active = 1
		UNION SELECT p.parent_id FROM parents p JOIN class_members m ON m.user_id = p.scholar_id AND m.role = 'scholar'
		JOIN users s ON s.id = p.scholar_id AND s.active = 1 JOIN users u ON u.id = p.parent_id AND u.active = 1
		WHERE m.class_id = ?1`, classID)
}

// ClassMentorIDs lists a class's active mentors.
func (s *Store) ClassMentorIDs(classID int64) ([]int64, error) {
	return s.ids(`SELECT m.user_id FROM class_members m JOIN users u ON u.id = m.user_id
		WHERE m.class_id = ? AND m.role = 'mentor' AND u.active = 1`, classID)
}

// ClassScholarIDs lists a class's active scholars.
func (s *Store) ClassScholarIDs(classID int64) ([]int64, error) {
	return s.ids(`SELECT m.user_id FROM class_members m JOIN users u ON u.id = m.user_id
		WHERE m.class_id = ? AND m.role = 'scholar' AND u.active = 1`, classID)
}

// ParentIDs lists a scholar's active parents.
func (s *Store) ParentIDs(scholarID int64) ([]int64, error) {
	return s.ids(`SELECT p.parent_id FROM parents p JOIN users u ON u.id = p.parent_id WHERE p.scholar_id = ? AND u.active = 1`, scholarID)
}

// ChannelMemberIDs lists a group chat's active members.
func (s *Store) ChannelMemberIDs(channelID int64) ([]int64, error) {
	return s.ids(`SELECT m.user_id FROM channel_members m JOIN users u ON u.id = m.user_id WHERE m.channel_id = ? AND u.active = 1`, channelID)
}

func (s *Store) ids(q string, args ...any) ([]int64, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --------------------------------------------------------------- reminders

// UnannouncedAssignments lists published assignments in current classes that
// nobody has been told about yet.
func (s *Store) UnannouncedAssignments(now int64) ([]*Assignment, error) {
	rows, err := s.db.Query(`SELECT `+assignmentCols+` FROM assignments a JOIN classes c ON c.id = a.class_id
		WHERE a.announced = 0 AND a.publish_at <> 0 AND a.publish_at <= ? AND c.archived = 0 ORDER BY a.publish_at, a.id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkAnnounced records that people were told about an assignment. It
// reports false if someone else already did.
func (s *Store) MarkAnnounced(id int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE assignments SET announced = 1 WHERE id = ? AND announced = 0`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DueOn lists published assignments in current classes due on a date.
func (s *Store) DueOn(date string, now int64) ([]*Assignment, error) {
	rows, err := s.db.Query(`SELECT `+assignmentCols+` FROM assignments a JOIN classes c ON c.id = a.class_id
		WHERE a.due_date = ? AND a.publish_at <> 0 AND a.publish_at <= ? AND c.archived = 0 ORDER BY a.id`, date, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
