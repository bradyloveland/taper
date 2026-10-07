package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/bradyloveland/taper/internal/auth"
)

// ResetFor is how long a password reset link works.
const ResetFor = time.Hour

// CreatePasswordReset makes a single-use reset link token for a user.
// Earlier unused tokens for that user stop working.
func (s *Store) CreatePasswordReset(userID int64) (string, error) {
	token := auth.Token(32)
	now := s.now()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM password_resets WHERE user_id = ?`, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO password_resets (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		auth.HashToken(token), userID, now, now+int64(ResetFor/time.Second)); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// PasswordResetUser returns the active user a reset token belongs to, if the
// token is unused and unexpired.
func (s *Store) PasswordResetUser(token string) (*User, error) {
	if token == "" {
		return nil, ErrNotFound
	}
	var id int64
	err := s.db.QueryRow(`SELECT user_id FROM password_resets WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		auth.HashToken(token), s.now()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u, err := s.GetUser(id)
	if err != nil {
		return nil, err
	}
	if !u.Active {
		return nil, ErrNotFound
	}
	return u, nil
}

// UsePasswordReset marks a token used. It reports false if it was already
// used or has expired.
func (s *Store) UsePasswordReset(token string) (bool, error) {
	res, err := s.db.Exec(`UPDATE password_resets SET used_at = ? WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		s.now(), auth.HashToken(token), s.now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
