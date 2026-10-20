package store

import "strings"

// PushSub is a device that gets notifications by web push.
type PushSub struct {
	ID          int64
	UserID      int64
	SessionHash string
	Endpoint    string
	P256dh      string // base64url
	Auth        string // base64url
	UserAgent   string
	CreatedAt   int64
	LastOKAt    int64
	Failures    int
}

const pushCols = `id, user_id, session_hash, endpoint, p256dh, auth, user_agent, created_at, last_ok_at, failures`

// SavePushSub records a device's subscription for the session that turned it
// on. A browser keeps its endpoint, so saving it again (after signing in as
// someone else, say) moves it to the new session.
func (s *Store) SavePushSub(p *PushSub) error {
	p.CreatedAt = s.now()
	_, err := s.db.Exec(`INSERT INTO push_subs (user_id, session_hash, endpoint, p256dh, auth, user_agent, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET user_id = excluded.user_id, session_hash = excluded.session_hash,
			p256dh = excluded.p256dh, auth = excluded.auth, user_agent = excluded.user_agent, failures = 0`,
		p.UserID, p.SessionHash, p.Endpoint, p.P256dh, p.Auth, p.UserAgent, p.CreatedAt)
	return err
}

// DeletePushSub forgets a device, if it's the user's.
func (s *Store) DeletePushSub(userID int64, endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM push_subs WHERE user_id = ? AND endpoint = ?`, userID, endpoint)
	return err
}

// DeletePushSubByID forgets a device the push service says is gone.
func (s *Store) DeletePushSubByID(id int64) error {
	_, err := s.db.Exec(`DELETE FROM push_subs WHERE id = ?`, id)
	return err
}

// DeleteUserPushSubs forgets all of a user's devices.
func (s *Store) DeleteUserPushSubs(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM push_subs WHERE user_id = ?`, userID)
	return err
}

// PushSubsFor lists a user's devices whose sessions are still signed in.
func (s *Store) PushSubsFor(userID int64) ([]*PushSub, error) {
	rows, err := s.db.Query(`SELECT `+prefixed("p.", pushCols)+` FROM push_subs p
		JOIN sessions x ON x.token_hash = p.session_hash AND x.expires_at > ?
		JOIN users u ON u.id = p.user_id AND u.active = 1
		WHERE p.user_id = ? ORDER BY p.created_at`, s.now(), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*PushSub
	for rows.Next() {
		p := &PushSub{}
		if err := rows.Scan(&p.ID, &p.UserID, &p.SessionHash, &p.Endpoint, &p.P256dh, &p.Auth, &p.UserAgent, &p.CreatedAt,
			&p.LastOKAt, &p.Failures); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PushResult records how a push went. After many failures in a row the
// device is forgotten.
func (s *Store) PushResult(id int64, ok bool) error {
	if ok {
		_, err := s.db.Exec(`UPDATE push_subs SET last_ok_at = ?, failures = 0 WHERE id = ?`, s.now(), id)
		return err
	}
	if _, err := s.db.Exec(`UPDATE push_subs SET failures = failures + 1 WHERE id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM push_subs WHERE id = ? AND failures >= 20`, id)
	return err
}

// prefixed qualifies a list of columns with a table alias, like "p.".
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
