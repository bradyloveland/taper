package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/bradyloveland/taper/internal/auth"
)

// Session lengths. A remembered session lasts RememberFor after it was last
// used; otherwise it ends after ShortFor (and the browser forgets the cookie
// when it closes), which suits shared school computers.
const (
	RememberFor = 30 * 24 * time.Hour
	ShortFor    = 12 * time.Hour
	// touchEvery limits how often last_seen_at is written.
	touchEvery = 5 * time.Minute
)

// Session is a signed-in browser.
type Session struct {
	TokenHash  string
	UserID     int64
	CSRF       string
	Remember   bool
	CreatedAt  int64
	LastSeenAt int64
	ExpiresAt  int64
	UserAgent  string
	IP         string
}

func (s *Store) expiry(remember bool, from int64) int64 {
	if remember {
		return from + int64(RememberFor/time.Second)
	}
	return from + int64(ShortFor/time.Second)
}

// CreateSession signs a user in and returns the token for the cookie.
func (s *Store) CreateSession(userID int64, remember bool, userAgent, ip string) (string, *Session, error) {
	token := auth.Token(32)
	now := s.now()
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	sess := &Session{TokenHash: auth.HashToken(token), UserID: userID, CSRF: auth.Token(24), Remember: remember,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: s.expiry(remember, now), UserAgent: userAgent, IP: ip}
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, csrf, remember, created_at, last_seen_at,
		expires_at, user_agent, ip) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, sess.TokenHash, sess.UserID, sess.CSRF,
		sess.Remember, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt, sess.UserAgent, sess.IP)
	if err != nil {
		return "", nil, err
	}
	return token, sess, nil
}

// LookupSession returns the session and its active user for a cookie token,
// extending the session as it's used. It returns ErrNotFound for unknown or
// expired sessions and for deactivated users.
func (s *Store) LookupSession(token string) (*Session, *User, error) {
	if token == "" {
		return nil, nil, ErrNotFound
	}
	sess := &Session{}
	err := s.db.QueryRow(`SELECT token_hash, user_id, csrf, remember, created_at, last_seen_at, expires_at,
		user_agent, ip FROM sessions WHERE token_hash = ?`, auth.HashToken(token)).Scan(&sess.TokenHash,
		&sess.UserID, &sess.CSRF, &sess.Remember, &sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt,
		&sess.UserAgent, &sess.IP)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	if now >= sess.ExpiresAt {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, sess.TokenHash)
		return nil, nil, ErrNotFound
	}
	u, err := s.GetUser(sess.UserID)
	if err != nil {
		return nil, nil, err
	}
	if !u.Active {
		return nil, nil, ErrNotFound
	}
	if now-sess.LastSeenAt >= int64(touchEvery/time.Second) {
		sess.LastSeenAt = now
		// Short sessions keep their fixed end; remembered ones slide.
		if sess.Remember {
			sess.ExpiresAt = s.expiry(true, now)
		}
		_, _ = s.db.Exec(`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE token_hash = ?`,
			sess.LastSeenAt, sess.ExpiresAt, sess.TokenHash)
	}
	return sess, u, nil
}

// DeleteSession signs one browser out.
func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// DeleteUserSessions signs a user out everywhere except keepHash ("" for everywhere).
func (s *Store) DeleteUserSessions(userID int64, keepHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, keepHash)
	return err
}

// UserSessions lists a user's sessions, most recently used first.
func (s *Store) UserSessions(userID int64) ([]*Session, error) {
	rows, err := s.db.Query(`SELECT token_hash, user_id, csrf, remember, created_at, last_seen_at, expires_at,
		user_agent, ip FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY last_seen_at DESC`, userID, s.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		sess := &Session{}
		if err := rows.Scan(&sess.TokenHash, &sess.UserID, &sess.CSRF, &sess.Remember, &sess.CreatedAt,
			&sess.LastSeenAt, &sess.ExpiresAt, &sess.UserAgent, &sess.IP); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// PruneSessions removes expired sessions.
func (s *Store) PruneSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, s.now())
	return err
}
