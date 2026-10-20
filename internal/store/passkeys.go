package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
)

// Passkey is a passkey someone added to their account.
type Passkey struct {
	ID         int64
	UserID     int64
	CredID     string // base64url
	PublicKey  []byte
	Alg        int
	SignCount  uint32
	BackedUp   bool
	Name       string
	CreatedAt  int64
	LastUsedAt int64
}

const passkeyCols = `id, user_id, cred_id, public_key, alg, sign_count, backed_up, name, created_at, last_used_at`

func scanPasskey(row interface{ Scan(...any) error }) (*Passkey, error) {
	p := &Passkey{}
	err := row.Scan(&p.ID, &p.UserID, &p.CredID, &p.PublicKey, &p.Alg, &p.SignCount, &p.BackedUp, &p.Name, &p.CreatedAt, &p.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// PasskeyHandle returns the random ID a person's passkeys carry, making it
// the first time.
func (s *Store) PasskeyHandle(userID int64) (string, error) {
	var h string
	if err := s.db.QueryRow(`SELECT passkey_handle FROM users WHERE id = ?`, userID).Scan(&h); err != nil {
		return "", err
	}
	if h != "" {
		return h, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	h = base64.RawURLEncoding.EncodeToString(b)
	if _, err := s.db.Exec(`UPDATE users SET passkey_handle = ? WHERE id = ? AND passkey_handle = ''`, h, userID); err != nil {
		return "", err
	}
	err := s.db.QueryRow(`SELECT passkey_handle FROM users WHERE id = ?`, userID).Scan(&h)
	return h, err
}

// UserByPasskeyHandle finds the person a passkey belongs to.
func (s *Store) UserByPasskeyHandle(handle string) (*User, error) {
	if handle == "" {
		return nil, ErrNotFound
	}
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE passkey_handle = ?`, handle))
}

// ErrPasskeyTaken means the passkey is already on an account.
var ErrPasskeyTaken = errors.New("passkey already added")

// AddPasskey saves a new passkey.
func (s *Store) AddPasskey(p *Passkey) error {
	p.CreatedAt = s.now()
	res, err := s.db.Exec(`INSERT INTO passkeys (user_id, cred_id, public_key, alg, sign_count, backed_up, name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, p.UserID, p.CredID, p.PublicKey, p.Alg, p.SignCount, p.BackedUp, p.Name, p.CreatedAt)
	if isUnique(err) {
		return ErrPasskeyTaken
	}
	if err != nil {
		return err
	}
	p.ID, err = res.LastInsertId()
	return err
}

// PasskeyByCredID finds a passkey by the ID the authenticator gave it.
func (s *Store) PasskeyByCredID(credID string) (*Passkey, error) {
	return scanPasskey(s.db.QueryRow(`SELECT `+passkeyCols+` FROM passkeys WHERE cred_id = ?`, credID))
}

// UserPasskeys lists a person's passkeys, oldest first.
func (s *Store) UserPasskeys(userID int64) ([]*Passkey, error) {
	rows, err := s.db.Query(`SELECT `+passkeyCols+` FROM passkeys WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPasskeys counts a person's passkeys.
func (s *Store) CountPasskeys(userID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM passkeys WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}

// UsePasskey records a sign-in with a passkey and its new counter.
func (s *Store) UsePasskey(id int64, count uint32) error {
	_, err := s.db.Exec(`UPDATE passkeys SET sign_count = ?, last_used_at = ? WHERE id = ?`, count, s.now(), id)
	return err
}

// DeletePasskey removes one of a person's passkeys.
func (s *Store) DeletePasskey(userID, id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM passkeys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteUserPasskeys removes all of a person's passkeys.
func (s *Store) DeleteUserPasskeys(userID int64) (int, error) {
	res, err := s.db.Exec(`DELETE FROM passkeys WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
