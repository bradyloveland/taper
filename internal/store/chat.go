package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Channel kinds.
const (
	ChannelCommunity = "community" // the whole school
	ChannelClass     = "class"     // one class
	ChannelGroup     = "group"     // a group chat with its own name and members
)

// Channel is a chat channel.
type Channel struct {
	ID        int64
	Kind      string
	ClassID   int64  // for class channels
	Name      string // for group chats
	Enabled   bool
	CreatedBy int64
	CreatedAt int64

	ClassName     string // filled in for class channels
	ClassColor    string
	ClassArchived bool
}

const channelCols = `ch.id, ch.kind, COALESCE(ch.class_id, 0), ch.name, ch.enabled, COALESCE(ch.created_by, 0), ch.created_at,
	COALESCE(c.name, ''), COALESCE(c.color, ''), COALESCE(c.archived, 0)`

func scanChannel(row interface{ Scan(...any) error }) (*Channel, error) {
	ch := &Channel{}
	err := row.Scan(&ch.ID, &ch.Kind, &ch.ClassID, &ch.Name, &ch.Enabled, &ch.CreatedBy, &ch.CreatedAt, &ch.ClassName,
		&ch.ClassColor, &ch.ClassArchived)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return ch, err
}

// GetChannel returns one channel.
func (s *Store) GetChannel(id int64) (*Channel, error) {
	return scanChannel(s.db.QueryRow(`SELECT `+channelCols+` FROM channels ch LEFT JOIN classes c ON c.id = ch.class_id
		WHERE ch.id = ?`, id))
}

// CommunityChannel returns the channel for the whole school.
func (s *Store) CommunityChannel() (*Channel, error) {
	return scanChannel(s.db.QueryRow(`SELECT ` + channelCols + ` FROM channels ch LEFT JOIN classes c ON c.id = ch.class_id
		WHERE ch.kind = 'community' ORDER BY ch.id LIMIT 1`))
}

// ClassChannel returns a class's channel, making it if needed.
func (s *Store) ClassChannel(classID int64) (*Channel, error) {
	if _, err := s.db.Exec(`INSERT INTO channels (kind, class_id, enabled, created_at) VALUES ('class', ?, 1, ?)
		ON CONFLICT (class_id) DO NOTHING`,
		classID, s.now()); err != nil {
		return nil, err
	}
	return scanChannel(s.db.QueryRow(`SELECT `+channelCols+` FROM channels ch JOIN classes c ON c.id = ch.class_id
		WHERE ch.class_id = ?`, classID))
}

// ClassChannels returns the channels that exist for classes, keyed by class.
func (s *Store) ClassChannels(classIDs []int64) (map[int64]*Channel, error) {
	out := map[int64]*Channel{}
	if len(classIDs) == 0 {
		return out, nil
	}
	marks, args := idList(classIDs)
	rows, err := s.db.Query(`SELECT `+channelCols+` FROM channels ch JOIN classes c ON c.id = ch.class_id
		WHERE ch.class_id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out[ch.ClassID] = ch
	}
	return out, rows.Err()
}

// CreateGroup makes a group chat with its first members.
func (s *Store) CreateGroup(name string, by int64, members []int64) (*Channel, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now()
	res, err := tx.Exec(`INSERT INTO channels (kind, name, enabled, created_by, created_at) VALUES ('group', ?, 1, ?, ?)`,
		name, nullID(by), now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, u := range members {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO channel_members (channel_id, user_id, added_at) VALUES (?, ?, ?)`, id, u, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetChannel(id)
}

// RenameChannel changes a group chat's name.
func (s *Store) RenameChannel(id int64, name string) error {
	_, err := s.db.Exec(`UPDATE channels SET name = ? WHERE id = ? AND kind = 'group'`, name, id)
	return err
}

// DeleteChannel removes a group chat with its messages. Remove the files
// that are no longer used with OrphanFiles.
func (s *Store) DeleteChannel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM channels WHERE id = ? AND kind = 'group'`, id)
	return err
}

// ListGroups returns group chats, by name: all of them, or with userID only
// those the person is in.
func (s *Store) ListGroups(userID int64) ([]*Channel, error) {
	q := `SELECT ` + channelCols + ` FROM channels ch LEFT JOIN classes c ON c.id = ch.class_id WHERE ch.kind = 'group'`
	var args []any
	if userID != 0 {
		q += ` AND EXISTS (SELECT 1 FROM channel_members m WHERE m.channel_id = ch.id AND m.user_id = ?)`
		args = append(args, userID)
	}
	q += ` ORDER BY ch.name COLLATE NOCASE, ch.id`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Channel
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// AddChannelMembers adds people to a group chat, returning how many were new.
func (s *Store) AddChannelMembers(channelID int64, userIDs []int64) (int, error) {
	n := 0
	for _, u := range userIDs {
		res, err := s.db.Exec(`INSERT OR IGNORE INTO channel_members (channel_id, user_id, added_at) VALUES (?, ?, ?)`,
			channelID, u, s.now())
		if err != nil {
			return n, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	return n, nil
}

// RemoveChannelMember takes someone out of a group chat.
func (s *Store) RemoveChannelMember(channelID, userID int64) error {
	_, err := s.db.Exec(`DELETE FROM channel_members WHERE channel_id = ? AND user_id = ?`, channelID, userID)
	return err
}

// InChannel reports whether someone is a member of a group chat.
func (s *Store) InChannel(channelID, userID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM channel_members WHERE channel_id = ? AND user_id = ?`, channelID, userID).Scan(&n)
	return n > 0, err
}

// ChannelMembers lists a group chat's active members by name.
func (s *Store) ChannelMembers(channelID int64) ([]*User, error) {
	rows, err := s.db.Query(`SELECT `+userColsAs("u")+` FROM channel_members m JOIN users u ON u.id = m.user_id
		WHERE m.channel_id = ? AND u.active = 1 ORDER BY u.display_name COLLATE NOCASE`, channelID)
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

// SetChannelEnabled turns a channel on or off.
func (s *Store) SetChannelEnabled(id int64, on bool) error {
	_, err := s.db.Exec(`UPDATE channels SET enabled = ? WHERE id = ?`, on, id)
	return err
}

// Message is one chat message.
type Message struct {
	ID        int64
	ChannelID int64
	UserID    int64
	Body      string
	CreatedAt int64
	DeletedAt int64
	DeletedBy int64

	AuthorName string // filled in from the author
	AuthorRole string
}

// Deleted reports whether the message was removed.
func (m *Message) Deleted() bool { return m.DeletedAt != 0 }

const messageCols = `m.id, m.channel_id, COALESCE(m.user_id, 0), m.body, m.created_at, m.deleted_at, COALESCE(m.deleted_by, 0),
	COALESCE(u.display_name, 'Someone'), COALESCE(u.role, '')`

func scanMessage(row interface{ Scan(...any) error }) (*Message, error) {
	m := &Message{}
	err := row.Scan(&m.ID, &m.ChannelID, &m.UserID, &m.Body, &m.CreatedAt, &m.DeletedAt, &m.DeletedBy, &m.AuthorName, &m.AuthorRole)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

func (s *Store) queryMessages(q string, args ...any) ([]*Message, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PostMessage adds a message.
func (s *Store) PostMessage(channelID, userID int64, body string) (*Message, error) {
	res, err := s.db.Exec(`INSERT INTO messages (channel_id, user_id, body, created_at) VALUES (?, ?, ?, ?)`,
		channelID, userID, body, s.now())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetMessage(id)
}

// GetMessage returns one message.
func (s *Store) GetMessage(id int64) (*Message, error) {
	return scanMessage(s.db.QueryRow(`SELECT `+messageCols+` FROM messages m LEFT JOIN users u ON u.id = m.user_id
		WHERE m.id = ?`, id))
}

// ListMessages returns up to limit messages in a channel before the message
// beforeID (0 for the newest), oldest first, and whether there are earlier ones.
func (s *Store) ListMessages(channelID, beforeID int64, limit int) ([]*Message, bool, error) {
	q := `SELECT ` + messageCols + ` FROM messages m LEFT JOIN users u ON u.id = m.user_id WHERE m.channel_id = ?`
	args := []any{channelID}
	if beforeID > 0 {
		q += ` AND m.id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY m.id DESC LIMIT ?`
	args = append(args, limit+1)
	list, err := s.queryMessages(q, args...)
	if err != nil {
		return nil, false, err
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
		list[i], list[j] = list[j], list[i]
	}
	return list, more, nil
}

// MessagesAfter returns up to limit messages in a channel after afterID,
// oldest first.
func (s *Store) MessagesAfter(channelID, afterID int64, limit int) ([]*Message, error) {
	return s.queryMessages(`SELECT `+messageCols+` FROM messages m LEFT JOIN users u ON u.id = m.user_id
		WHERE m.channel_id = ? AND m.id > ? ORDER BY m.id LIMIT ?`, channelID, afterID, limit)
}

// DeleteMessage removes a message's text, keeping a note that it was there.
func (s *Store) DeleteMessage(id, by int64) error {
	res, err := s.db.Exec(`UPDATE messages SET body = '', deleted_at = ?, deleted_by = ? WHERE id = ? AND deleted_at = 0`,
		s.now(), nullID(by), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkRead records that a person has read a channel up to a message.
func (s *Store) MarkRead(userID, channelID, lastID int64) error {
	_, err := s.db.Exec(`INSERT INTO channel_reads (user_id, channel_id, last_read) VALUES (?, ?, ?)
		ON CONFLICT (user_id, channel_id) DO UPDATE SET last_read = MAX(last_read, excluded.last_read)`, userID, channelID, lastID)
	return err
}

// LastRead returns how far a person has read in a channel.
func (s *Store) LastRead(userID, channelID int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT last_read FROM channel_reads WHERE user_id = ? AND channel_id = ?`, userID, channelID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func idList(ids []int64) (string, []any) {
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i], args[i] = "?", id
	}
	return strings.Join(marks, ","), args
}

// UnreadCounts counts others' messages a person hasn't read in each channel.
func (s *Store) UnreadCounts(userID int64, channelIDs []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(channelIDs) == 0 {
		return out, nil
	}
	marks, args := idList(channelIDs)
	rows, err := s.db.Query(`SELECT m.channel_id, COUNT(*) FROM messages m
		LEFT JOIN channel_reads r ON r.channel_id = m.channel_id AND r.user_id = ?
		WHERE m.channel_id IN (`+marks+`) AND m.id > COALESCE(r.last_read, 0) AND m.deleted_at = 0
		AND COALESCE(m.user_id, 0) <> ? GROUP BY m.channel_id`, append(append([]any{userID}, args...), userID)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// LatestMessages returns the newest message in each channel.
func (s *Store) LatestMessages(channelIDs []int64) (map[int64]*Message, error) {
	out := map[int64]*Message{}
	if len(channelIDs) == 0 {
		return out, nil
	}
	marks, args := idList(channelIDs)
	list, err := s.queryMessages(`SELECT `+messageCols+` FROM messages m LEFT JOIN users u ON u.id = m.user_id
		WHERE m.id IN (SELECT MAX(id) FROM messages WHERE channel_id IN (`+marks+`) GROUP BY channel_id)`, args...)
	if err != nil {
		return nil, err
	}
	for _, m := range list {
		out[m.ChannelID] = m
	}
	return out, nil
}

// Mute is someone who can't post in a channel.
type Mute struct {
	ChannelID int64
	UserID    int64
	Until     int64 // 0: until unmuted
	By        string
	CreatedAt int64
	Name      string // the muted person
}

// SetMute stops someone posting in a channel until a time (0 for until
// they're unmuted).
func (s *Store) SetMute(channelID, userID, until, by int64) error {
	_, err := s.db.Exec(`INSERT INTO chat_mutes (channel_id, user_id, until, by_user, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (channel_id, user_id) DO UPDATE SET until = excluded.until, by_user = excluded.by_user,
		created_at = excluded.created_at`, channelID, userID, until, nullID(by), s.now())
	return err
}

// Unmute lets someone post again.
func (s *Store) Unmute(channelID, userID int64) error {
	_, err := s.db.Exec(`DELETE FROM chat_mutes WHERE channel_id = ? AND user_id = ?`, channelID, userID)
	return err
}

const muteCols = `m.channel_id, m.user_id, m.until, COALESCE(b.display_name, ''), m.created_at, COALESCE(u.display_name, '')`

// MuteOf returns someone's mute in a channel if it's in force, or nil.
func (s *Store) MuteOf(channelID, userID int64) (*Mute, error) {
	mu := &Mute{}
	err := s.db.QueryRow(`SELECT `+muteCols+` FROM chat_mutes m LEFT JOIN users b ON b.id = m.by_user
		LEFT JOIN users u ON u.id = m.user_id WHERE m.channel_id = ? AND m.user_id = ? AND (m.until = 0 OR m.until > ?)`,
		channelID, userID, s.now()).Scan(&mu.ChannelID, &mu.UserID, &mu.Until, &mu.By, &mu.CreatedAt, &mu.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return mu, err
}

// ListMutes lists the mutes in force in a channel.
func (s *Store) ListMutes(channelID int64) ([]*Mute, error) {
	rows, err := s.db.Query(`SELECT `+muteCols+` FROM chat_mutes m LEFT JOIN users b ON b.id = m.by_user
		LEFT JOIN users u ON u.id = m.user_id WHERE m.channel_id = ? AND (m.until = 0 OR m.until > ?)
		ORDER BY u.display_name COLLATE NOCASE`, channelID, s.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Mute
	for rows.Next() {
		mu := &Mute{}
		if err := rows.Scan(&mu.ChannelID, &mu.UserID, &mu.Until, &mu.By, &mu.CreatedAt, &mu.Name); err != nil {
			return nil, err
		}
		out = append(out, mu)
	}
	return out, rows.Err()
}
