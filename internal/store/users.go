package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Roles.
const (
	RoleAdmin   = "admin"
	RoleMentor  = "mentor"
	RoleScholar = "scholar"
)

// Roles lists every role, most powerful first.
var Roles = []string{RoleAdmin, RoleMentor, RoleScholar}

// RoleLabel is a role's name as shown in the interface.
func RoleLabel(role string) string {
	switch role {
	case RoleAdmin:
		return "Admin"
	case RoleMentor:
		return "Mentor"
	case RoleScholar:
		return "Scholar"
	}
	return role
}

// ValidRole reports whether role is one of Roles.
func ValidRole(role string) bool {
	for _, r := range Roles {
		if r == role {
			return true
		}
	}
	return false
}

// User is a person who can sign in.
type User struct {
	ID                 int64
	Username           string
	DisplayName        string
	Email              string
	Role               string
	PasswordHash       string
	MustChangePassword bool
	Active             bool
	CreatedAt          int64
	UpdatedAt          int64
	LastLoginAt        int64  // 0 if never
	TOTPSecret         string // encrypted; "" if two-step sign-in was never set up
	TOTPEnabled        bool
	TOTPLastStep       int64
}

// IsAdmin reports whether the user is an admin.
func (u *User) IsAdmin() bool { return u.Role == RoleAdmin }

// IsMentor reports whether the user can do what mentors do (admins can too).
func (u *User) IsMentor() bool { return u.Role == RoleMentor || u.Role == RoleAdmin }

// RoleLabel is the user's role as shown in the interface.
func (u *User) RoleLabel() string { return RoleLabel(u.Role) }

// Initials are up to two letters for the avatar.
func (u *User) Initials() string {
	var out []rune
	for _, f := range strings.Fields(u.DisplayName) {
		out = append(out, []rune(strings.ToUpper(f))[0])
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

const userCols = `id, username, display_name, email, role, password_hash, must_change_password, active,
	created_at, updated_at, COALESCE(last_login_at, 0), totp_secret, totp_enabled, totp_last_step`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	u := &User{}
	err := row.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Role, &u.PasswordHash,
		&u.MustChangePassword, &u.Active, &u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt, &u.TOTPSecret, &u.TOTPEnabled,
		&u.TOTPLastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// ErrUsernameTaken means another account already has that username.
var ErrUsernameTaken = errors.New("username taken")

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CreateUser adds a user and sets u.ID.
func (s *Store) CreateUser(u *User) error {
	now := s.now()
	u.CreatedAt, u.UpdatedAt = now, now
	res, err := s.db.Exec(`INSERT INTO users (username, display_name, email, role, password_hash,
		must_change_password, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.DisplayName, u.Email, u.Role, u.PasswordHash, u.MustChangePassword, u.Active, now, now)
	if isUnique(err) {
		return ErrUsernameTaken
	}
	if err != nil {
		return err
	}
	u.ID, err = res.LastInsertId()
	return err
}

// UpdateUser saves a user's profile, role and status (not the password).
func (s *Store) UpdateUser(u *User) error {
	u.UpdatedAt = s.now()
	res, err := s.db.Exec(`UPDATE users SET username = ?, display_name = ?, email = ?, role = ?, active = ?,
		updated_at = ? WHERE id = ?`, u.Username, u.DisplayName, u.Email, u.Role, u.Active, u.UpdatedAt, u.ID)
	if isUnique(err) {
		return ErrUsernameTaken
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPassword replaces a user's password hash. mustChange makes them pick a
// new one when they next sign in.
func (s *Store) SetPassword(id int64, hash string, mustChange bool) error {
	res, err := s.db.Exec(`UPDATE users SET password_hash = ?, must_change_password = ?, updated_at = ? WHERE id = ?`,
		hash, mustChange, s.now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchLogin records a successful sign-in.
func (s *Store) TouchLogin(id int64) error {
	_, err := s.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, s.now(), id)
	return err
}

// GetUser returns a user by ID.
func (s *Store) GetUser(id int64) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// GetUserByUsername returns a user by username (any case).
func (s *Store) GetUserByUsername(username string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE username = ?`, strings.TrimSpace(username)))
}

// UserFilter narrows ListUsers.
type UserFilter struct {
	Role   string // "" for every role
	Query  string // matches name, username or email
	Status string // "active" (default), "inactive" or "all"
}

// ListUsers returns users sorted by name.
func (s *Store) ListUsers(f UserFilter) ([]*User, error) {
	q := `SELECT ` + userCols + ` FROM users WHERE 1 = 1`
	var args []any
	if f.Role != "" {
		q += ` AND role = ?`
		args = append(args, f.Role)
	}
	switch f.Status {
	case "all":
	case "inactive":
		q += ` AND active = 0`
	default:
		q += ` AND active = 1`
	}
	if t := strings.TrimSpace(f.Query); t != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(t) + "%"
		q += ` AND (display_name LIKE ? ESCAPE '\' OR username LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\')`
		args = append(args, like, like, like)
	}
	q += ` ORDER BY display_name COLLATE NOCASE, username COLLATE NOCASE`
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

// CountUsers returns the number of users.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// RoleCounts returns the number of active users in each role.
func (s *Store) RoleCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT role, COUNT(*) FROM users WHERE active = 1 GROUP BY role`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var role string
		var n int
		if err := rows.Scan(&role, &n); err != nil {
			return nil, err
		}
		out[role] = n
	}
	return out, rows.Err()
}

// ActiveAdmins returns the number of active admins.
func (s *Store) ActiveAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND active = 1`).Scan(&n)
	return n, err
}

// ------------------------------------------------------- two-step sign-in

// EnableTOTP turns on two-step sign-in with an (encrypted) secret, replacing
// any recovery codes with the given hashes.
func (s *Store) EnableTOTP(id int64, sealedSecret string, step int64, codeHashes []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE users SET totp_secret = ?, totp_enabled = 1, totp_last_step = ?, updated_at = ? WHERE id = ?`,
		sealedSecret, step, s.now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := replaceCodes(tx, id, codeHashes); err != nil {
		return err
	}
	return tx.Commit()
}

// DisableTOTP turns off two-step sign-in and removes the recovery codes.
func (s *Store) DisableTOTP(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET totp_secret = '', totp_enabled = 0, totp_last_step = 0, updated_at = ? WHERE id = ?`,
		s.now(), id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// UseTOTPStep records that the code for step was used. It reports false if
// that step (or a later one) was already used, so a code works only once.
func (s *Store) UseTOTPStep(id, step int64) (bool, error) {
	res, err := s.db.Exec(`UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, id, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ReplaceRecoveryCodes swaps a user's recovery codes for new ones.
func (s *Store) ReplaceRecoveryCodes(id int64, codeHashes []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceCodes(tx, id, codeHashes); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceCodes(tx *sql.Tx, id int64, hashes []string) error {
	if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE user_id = ?`, id); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.Exec(`INSERT INTO recovery_codes (user_id, code_hash) VALUES (?, ?)`, id, h); err != nil {
			return err
		}
	}
	return nil
}

// UseRecoveryCode marks a recovery code used. It reports false if the code
// isn't one of the user's unused codes.
func (s *Store) UseRecoveryCode(id int64, codeHash string) (bool, error) {
	res, err := s.db.Exec(`UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`,
		s.now(), id, codeHash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecoveryCodesLeft counts a user's unused recovery codes.
func (s *Store) RecoveryCodesLeft(id int64) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM recovery_codes WHERE user_id = ? AND used_at IS NULL`, id).Scan(&n)
	return n, err
}

// ListAdmins returns active admins, for notices sent to all of them.
func (s *Store) ListAdmins() ([]*User, error) {
	return s.ListUsers(UserFilter{Role: RoleAdmin})
}

// UsersByEmail returns active users with that email address (any case).
func (s *Store) UsersByEmail(email string) ([]*User, error) {
	rows, err := s.db.Query(`SELECT `+userCols+` FROM users WHERE active = 1 AND email <> '' AND email = ? COLLATE NOCASE`,
		strings.TrimSpace(email))
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
