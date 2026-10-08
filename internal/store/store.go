// Package store keeps Taper's data in SQLite. Schema changes are numbered
// migrations in migrations/, applied in order at startup.
package store

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound means the requested row doesn't exist.
var ErrNotFound = errors.New("not found")

// Store wraps the database. It's safe for concurrent use.
type Store struct {
	db  *sql.DB
	Now func() time.Time
}

// Open opens (creating if needed) the database at path and applies any
// pending migrations.
func Open(path string) (*Store, error) { return openTo(path, 0) }

// openTo opens the database, applying migrations up to version upTo (0 for
// all). Tests use it to check a migration against older data.
func openTo(path string, upTo int) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection serialises writes, so there's never a "database is
	// locked" error inside the app. A school's workload is small.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, Now: time.Now}
	if err := s.migrate(upTo); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) now() int64 { return s.Now().Unix() }

// SchemaVersion returns the newest applied migration.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

func (s *Store) migrate(upTo int) error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	current, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		v, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: bad name", base)
		}
		if v <= current || (upTo > 0 && v > upTo) {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if err := s.runMigration(base, v, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// fkOff marks a migration that rebuilds tables, which SQLite says to do with
// foreign keys off (they're checked before it's committed).
const fkOff = "-- taper:foreign-keys-off"

func (s *Store) runMigration(base string, v int, body string) error {
	rebuild := strings.HasPrefix(body, fkOff)
	if rebuild {
		// The store uses one connection, so this applies to the migration.
		if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
			return err
		}
		defer s.db.Exec(`PRAGMA foreign_keys = ON`)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(body); err != nil {
		return fmt.Errorf("migration %s: %w", base, err)
	}
	if rebuild {
		rows, err := tx.Query(`PRAGMA foreign_key_check`)
		if err != nil {
			return err
		}
		bad := rows.Next()
		rows.Close()
		if bad {
			return fmt.Errorf("migration %s: it would break links between tables", base)
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, v, s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

// Backup writes a consistent copy of the database to path.
func (s *Store) Backup(path string) error {
	_, err := s.db.Exec(`VACUUM INTO ?`, path)
	return err
}

// ---------------------------------------------------------------- settings

// GetSetting decodes the setting into dst. It reports false if it isn't set.
func (s *Store) GetSetting(key string, dst any) (bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), dst)
}

// SetSetting stores value (as JSON) under key.
func (s *Store) SetSetting(key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, string(raw))
	return err
}

// SchoolName returns the school's name, or "" before setup.
func (s *Store) SchoolName() string {
	var name string
	_, _ = s.GetSetting("school_name", &name)
	return name
}
