package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/cal"
)

// CalEvent is a calendar event: on the school calendar (ClassID 0) or a
// class's. Dates and times are the school's local time.
type CalEvent struct {
	ID          int64
	ClassID     int64 // 0: school calendar
	Title       string
	Description string
	Location    string
	Closed      bool // no school that day
	StartDate   string
	StartTime   string // "" for all day
	EndDate     string
	EndTime     string
	Repeat      string
	RepeatDays  string
	RepeatUntil string
	UID         string
	Sequence    int
	CreatedBy   int64
	CreatedAt   int64
	UpdatedAt   int64

	ClassName  string // filled in from the class
	ClassColor string
	Skips      map[string]bool
}

// AllDay reports whether the event has no times.
func (e *CalEvent) AllDay() bool { return e.StartTime == "" }

// Rule returns the event in the form package cal works with.
func (e *CalEvent) Rule() (*cal.Event, error) {
	start, err := cal.ParseDate(e.StartDate)
	if err != nil {
		return nil, err
	}
	end, err := cal.ParseDate(e.EndDate)
	if err != nil {
		return nil, err
	}
	r := &cal.Event{StartDate: start, EndDate: end, StartMin: -1, EndMin: -1, Repeat: e.Repeat,
		Days: cal.ParseDays(e.RepeatDays), Skips: e.Skips}
	if !e.AllDay() {
		if r.StartMin, err = cal.ParseClock(e.StartTime); err != nil {
			return nil, err
		}
		if r.EndMin, err = cal.ParseClock(e.EndTime); err != nil {
			return nil, err
		}
	}
	if e.RepeatUntil != "" {
		if r.Until, err = cal.ParseDate(e.RepeatUntil); err != nil {
			return nil, err
		}
	}
	return r, nil
}

const eventCols = `e.id, COALESCE(e.class_id, 0), e.title, e.description, e.location, e.closed, e.start_date, e.start_time,
	e.end_date, e.end_time, e.repeat, e.repeat_days, e.repeat_until, e.uid, e.sequence, COALESCE(e.created_by, 0),
	e.created_at, e.updated_at, COALESCE(c.name, ''), COALESCE(c.color, '')`

func scanEvent(row interface{ Scan(...any) error }) (*CalEvent, error) {
	e := &CalEvent{}
	err := row.Scan(&e.ID, &e.ClassID, &e.Title, &e.Description, &e.Location, &e.Closed, &e.StartDate, &e.StartTime,
		&e.EndDate, &e.EndTime, &e.Repeat, &e.RepeatDays, &e.RepeatUntil, &e.UID, &e.Sequence, &e.CreatedBy,
		&e.CreatedAt, &e.UpdatedAt, &e.ClassName, &e.ClassColor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return e, err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// CreateEvent adds an event and sets e.ID and e.UID.
func (s *Store) CreateEvent(e *CalEvent) error {
	now := s.now()
	e.CreatedAt, e.UpdatedAt = now, now
	e.UID = strings.ToLower(auth.Token(16)) + "@taper"
	res, err := s.db.Exec(`INSERT INTO events (class_id, title, description, location, closed, start_date, start_time,
		end_date, end_time, repeat, repeat_days, repeat_until, uid, sequence, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`, nullID(e.ClassID), e.Title, e.Description, e.Location,
		e.Closed, e.StartDate, e.StartTime, e.EndDate, e.EndTime, e.Repeat, e.RepeatDays, e.RepeatUntil, e.UID,
		nullID(e.CreatedBy), now, now)
	if err != nil {
		return err
	}
	e.ID, err = res.LastInsertId()
	return err
}

// UpdateEvent saves an event's details (not its calendar) and bumps its
// sequence so calendar apps take the change.
func (s *Store) UpdateEvent(e *CalEvent) error {
	e.UpdatedAt = s.now()
	res, err := s.db.Exec(`UPDATE events SET title = ?, description = ?, location = ?, closed = ?, start_date = ?,
		start_time = ?, end_date = ?, end_time = ?, repeat = ?, repeat_days = ?, repeat_until = ?, sequence = sequence + 1,
		updated_at = ? WHERE id = ?`, e.Title, e.Description, e.Location, e.Closed, e.StartDate, e.StartTime, e.EndDate,
		e.EndTime, e.Repeat, e.RepeatDays, e.RepeatUntil, e.UpdatedAt, e.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteEvent removes an event and all its occurrences.
func (s *Store) DeleteEvent(id int64) error {
	_, err := s.db.Exec(`DELETE FROM events WHERE id = ?`, id)
	return err
}

// SkipDate leaves one date out of a repeating event.
func (s *Store) SkipDate(id int64, date string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO event_skips (event_id, date) VALUES (?, ?)`, id, date); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE events SET sequence = sequence + 1, updated_at = ? WHERE id = ?`, s.now(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// GetEvent returns one event with its skipped dates.
func (s *Store) GetEvent(id int64) (*CalEvent, error) {
	e, err := scanEvent(s.db.QueryRow(`SELECT `+eventCols+` FROM events e LEFT JOIN classes c ON c.id = e.class_id
		WHERE e.id = ?`, id))
	if err != nil {
		return nil, err
	}
	return e, s.fillSkips([]*CalEvent{e})
}

// EventQuery chooses which calendars to read.
type EventQuery struct {
	School   bool    // include the school calendar
	ClassIDs []int64 // include these classes' calendars
	From, To string  // dates, inclusive
}

// ListEvents returns events on the chosen calendars that might have an
// occurrence between From and To. Expand them with Rule().Occurrences.
func (s *Store) ListEvents(q EventQuery) ([]*CalEvent, error) {
	var where []string
	var args []any
	if q.School {
		where = append(where, "e.class_id IS NULL")
	}
	if len(q.ClassIDs) > 0 {
		marks := make([]string, len(q.ClassIDs))
		for i, id := range q.ClassIDs {
			marks[i] = "?"
			args = append(args, id)
		}
		where = append(where, "e.class_id IN ("+strings.Join(marks, ",")+")")
	}
	if len(where) == 0 {
		return nil, nil
	}
	sqlq := `SELECT ` + eventCols + ` FROM events e LEFT JOIN classes c ON c.id = e.class_id
		WHERE (` + strings.Join(where, " OR ") + `) AND e.start_date <= ?
		AND (e.repeat <> '' OR e.end_date >= ?) AND (e.repeat_until = '' OR e.repeat_until >= ?)
		ORDER BY e.start_date, e.start_time, e.id`
	// Repeating multi-day events can start before From and still reach it;
	// allow a generous margin and let the occurrence check decide.
	margin := q.From
	if t, err := time.Parse("2006-01-02", q.From); err == nil {
		margin = t.AddDate(0, 0, -62).Format("2006-01-02")
	}
	args = append(args, q.To, q.From, margin)
	rows, err := s.db.Query(sqlq, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CalEvent
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.fillSkips(out)
}

func (s *Store) fillSkips(list []*CalEvent) error {
	if len(list) == 0 {
		return nil
	}
	byID := map[int64]*CalEvent{}
	marks := make([]string, 0, len(list))
	args := make([]any, 0, len(list))
	for _, e := range list {
		e.Skips = map[string]bool{}
		if e.Repeat == "" {
			continue
		}
		byID[e.ID] = e
		marks = append(marks, "?")
		args = append(args, e.ID)
	}
	if len(marks) == 0 {
		return nil
	}
	rows, err := s.db.Query(`SELECT event_id, date FROM event_skips WHERE event_id IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var date string
		if err := rows.Scan(&id, &date); err != nil {
			return err
		}
		byID[id].Skips[date] = true
	}
	return rows.Err()
}

// ------------------------------------------------------------ feeds

// SetCalendarFeed stores a person's (new) subscription token.
func (s *Store) SetCalendarFeed(userID int64, tokenHash, sealed string) error {
	_, err := s.db.Exec(`INSERT INTO calendar_feeds (user_id, token_hash, token_sealed, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET token_hash = excluded.token_hash, token_sealed = excluded.token_sealed,
		created_at = excluded.created_at`, userID, tokenHash, sealed, s.now())
	return err
}

// CalendarFeed returns a person's sealed subscription token, or "" if they
// don't have one yet.
func (s *Store) CalendarFeed(userID int64) (string, error) {
	var sealed string
	err := s.db.QueryRow(`SELECT token_sealed FROM calendar_feeds WHERE user_id = ?`, userID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return sealed, err
}

// UserByCalendarFeed returns the active user a subscription token belongs to.
func (s *Store) UserByCalendarFeed(tokenHash string) (*User, error) {
	var id int64
	err := s.db.QueryRow(`SELECT user_id FROM calendar_feeds WHERE token_hash = ?`, tokenHash).Scan(&id)
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
