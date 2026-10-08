package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Assignment is work set for a class.
type Assignment struct {
	ID           int64
	ClassID      int64
	Title        string
	Instructions string
	DueDate      string // YYYY-MM-DD, or "" for none
	DueTime      string // HH:MM, or "" for the end of the day
	PublishAt    int64  // 0: a draft
	WorkOnline   bool
	AllowFiles   bool
	CreatedBy    int64
	CreatedAt    int64
	UpdatedAt    int64

	ClassName  string
	ClassColor string
}

// Published reports whether scholars can see it at now (unix seconds).
func (a *Assignment) Published(now int64) bool { return a.PublishAt != 0 && a.PublishAt <= now }

// Draft reports whether it hasn't been scheduled at all.
func (a *Assignment) Draft() bool { return a.PublishAt == 0 }

const assignmentCols = `a.id, a.class_id, a.title, a.instructions, a.due_date, a.due_time, a.publish_at, a.work_online,
	a.allow_files, COALESCE(a.created_by, 0), a.created_at, a.updated_at, c.name, c.color`

func scanAssignment(row interface{ Scan(...any) error }) (*Assignment, error) {
	a := &Assignment{}
	err := row.Scan(&a.ID, &a.ClassID, &a.Title, &a.Instructions, &a.DueDate, &a.DueTime, &a.PublishAt, &a.WorkOnline,
		&a.AllowFiles, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &a.ClassName, &a.ClassColor)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// CreateAssignment adds an assignment and sets a.ID.
func (s *Store) CreateAssignment(a *Assignment) error {
	now := s.now()
	a.CreatedAt, a.UpdatedAt = now, now
	res, err := s.db.Exec(`INSERT INTO assignments (class_id, title, instructions, due_date, due_time, publish_at,
		work_online, allow_files, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ClassID, a.Title, a.Instructions, a.DueDate, a.DueTime, a.PublishAt, a.WorkOnline, a.AllowFiles,
		nullID(a.CreatedBy), now, now)
	if err != nil {
		return err
	}
	a.ID, err = res.LastInsertId()
	return err
}

// UpdateAssignment saves an assignment's details.
func (s *Store) UpdateAssignment(a *Assignment) error {
	a.UpdatedAt = s.now()
	res, err := s.db.Exec(`UPDATE assignments SET title = ?, instructions = ?, due_date = ?, due_time = ?, publish_at = ?,
		work_online = ?, allow_files = ?, updated_at = ? WHERE id = ?`, a.Title, a.Instructions, a.DueDate, a.DueTime,
		a.PublishAt, a.WorkOnline, a.AllowFiles, a.UpdatedAt, a.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAssignment removes an assignment and all work on it. Remove the
// files that are no longer used with OrphanFiles.
func (s *Store) DeleteAssignment(id int64) error {
	_, err := s.db.Exec(`DELETE FROM assignments WHERE id = ?`, id)
	return err
}

// GetAssignment returns one assignment.
func (s *Store) GetAssignment(id int64) (*Assignment, error) {
	return scanAssignment(s.db.QueryRow(`SELECT `+assignmentCols+` FROM assignments a JOIN classes c ON c.id = a.class_id
		WHERE a.id = ?`, id))
}

// AssignmentQuery chooses assignments.
type AssignmentQuery struct {
	ClassIDs      []int64
	PublishedOnly bool   // only ones scholars can see
	Now           int64  // for PublishedOnly
	DueFrom       string // with a due date on or after this (YYYY-MM-DD)
	DueTo         string // with a due date on or before this
}

// ListAssignments returns assignments in the classes, by due date (those
// without one last), then newest first.
func (s *Store) ListAssignments(q AssignmentQuery) ([]*Assignment, error) {
	if len(q.ClassIDs) == 0 {
		return nil, nil
	}
	marks := make([]string, len(q.ClassIDs))
	args := make([]any, 0, len(q.ClassIDs)+3)
	for i, id := range q.ClassIDs {
		marks[i] = "?"
		args = append(args, id)
	}
	sqlq := `SELECT ` + assignmentCols + ` FROM assignments a JOIN classes c ON c.id = a.class_id
		WHERE a.class_id IN (` + strings.Join(marks, ",") + `)`
	if q.PublishedOnly {
		sqlq += ` AND a.publish_at <> 0 AND a.publish_at <= ?`
		args = append(args, q.Now)
	}
	if q.DueFrom != "" {
		sqlq += ` AND a.due_date <> '' AND a.due_date >= ?`
		args = append(args, q.DueFrom)
	}
	if q.DueTo != "" {
		sqlq += ` AND a.due_date <> '' AND a.due_date <= ?`
		args = append(args, q.DueTo)
	}
	sqlq += ` ORDER BY a.due_date = '', a.due_date, a.due_time = '', a.due_time, a.created_at DESC, a.id DESC`
	rows, err := s.db.Query(sqlq, args...)
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

// ---------------------------------------------------------- submissions

// Submission statuses.
const (
	WorkDraft     = "draft"
	WorkTurnedIn  = "turned_in"
	WorkNeedsWork = "needs_work"
	WorkComplete  = "complete"
)

// Submission is a scholar's work on an assignment.
type Submission struct {
	ID           int64
	AssignmentID int64
	ScholarID    int64
	Body         string
	Status       string
	TurnedInAt   int64
	Feedback     string
	FeedbackBy   int64
	FeedbackAt   int64
	CreatedAt    int64
	UpdatedAt    int64
}

// Editable reports whether the scholar can change the work now.
func (w *Submission) Editable() bool { return w.Status == WorkDraft || w.Status == WorkNeedsWork }

const submissionCols = `id, assignment_id, scholar_id, body, status, turned_in_at, feedback, COALESCE(feedback_by, 0),
	feedback_at, created_at, updated_at`

func scanSubmission(row interface{ Scan(...any) error }) (*Submission, error) {
	w := &Submission{}
	err := row.Scan(&w.ID, &w.AssignmentID, &w.ScholarID, &w.Body, &w.Status, &w.TurnedInAt, &w.Feedback, &w.FeedbackBy,
		&w.FeedbackAt, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return w, err
}

// GetSubmission returns a scholar's work on an assignment, or ErrNotFound if
// they haven't started.
func (s *Store) GetSubmission(assignmentID, scholarID int64) (*Submission, error) {
	return scanSubmission(s.db.QueryRow(`SELECT `+submissionCols+` FROM submissions WHERE assignment_id = ? AND scholar_id = ?`,
		assignmentID, scholarID))
}

// GetSubmissionByID returns one piece of work.
func (s *Store) GetSubmissionByID(id int64) (*Submission, error) {
	return scanSubmission(s.db.QueryRow(`SELECT `+submissionCols+` FROM submissions WHERE id = ?`, id))
}

// StartSubmission returns a scholar's work, creating an empty draft if needed.
func (s *Store) StartSubmission(assignmentID, scholarID int64) (*Submission, error) {
	now := s.now()
	if _, err := s.db.Exec(`INSERT INTO submissions (assignment_id, scholar_id, created_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (assignment_id, scholar_id) DO NOTHING`, assignmentID, scholarID, now, now); err != nil {
		return nil, err
	}
	return s.GetSubmission(assignmentID, scholarID)
}

// SaveWork stores the written work, if it can still be changed.
func (s *Store) SaveWork(id int64, body string) error {
	res, err := s.db.Exec(`UPDATE submissions SET body = ?, updated_at = ? WHERE id = ? AND status IN ('draft', 'needs_work')`,
		body, s.now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrWrongStatus means the work isn't in a state that allows the change.
var ErrWrongStatus = errors.New("the work isn't in a state that allows that")

// History kinds besides the statuses themselves.
const (
	WorkTakenBack   = "taken_back"  // the scholar took it back to change it
	WorkReturned    = "returned"    // a mentor returned it to the scholar, not turned in
	WorkUncompleted = "uncompleted" // a mentor undid Complete, back to Turned in
)

// SetWorkStatus moves work from one of the from statuses to to, and records
// it in the history. Feedback, when given, replaces the latest feedback.
func (s *Store) SetWorkStatus(id int64, from []string, to string, by int64, feedback *string) error {
	kind := to
	if to == WorkDraft {
		kind = WorkTakenBack
	}
	return s.MoveWork(id, from, to, kind, by, feedback)
}

// MoveWork is SetWorkStatus with the history kind given. The turned-in time
// is set only when a scholar turns work in (kind WorkTurnedIn).
func (s *Store) MoveWork(id int64, from []string, to, kind string, by int64, feedback *string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now()
	marks := make([]string, len(from))
	args := []any{to, now}
	q := `UPDATE submissions SET status = ?, updated_at = ?`
	if kind == WorkTurnedIn {
		q += `, turned_in_at = ?`
		args = append(args, now)
	}
	if feedback != nil {
		q += `, feedback = ?, feedback_by = ?, feedback_at = ?`
		args = append(args, *feedback, nullID(by), now)
	}
	q += ` WHERE id = ? AND status IN (`
	args = append(args, id)
	for i, f := range from {
		marks[i] = "?"
		args = append(args, f)
	}
	q += strings.Join(marks, ",") + `)`
	res, err := tx.Exec(q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrWrongStatus
	}
	note := ""
	if feedback != nil {
		note = *feedback
	}
	if _, err := tx.Exec(`INSERT INTO submission_history (submission_id, kind, by_user, note, at) VALUES (?, ?, ?, ?, ?)`,
		id, kind, nullID(by), note, now); err != nil {
		return err
	}
	return tx.Commit()
}

// HistoryItem is one thing that happened to a piece of work.
type HistoryItem struct {
	Kind string
	By   string // the person's name
	Note string
	At   int64
}

// SubmissionHistory lists what happened to a piece of work, oldest first.
func (s *Store) SubmissionHistory(id int64) ([]HistoryItem, error) {
	rows, err := s.db.Query(`SELECT h.kind, COALESCE(u.display_name, ''), h.note, h.at FROM submission_history h
		LEFT JOIN users u ON u.id = h.by_user WHERE h.submission_id = ? ORDER BY h.at, h.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryItem
	for rows.Next() {
		var h HistoryItem
		if err := rows.Scan(&h.Kind, &h.By, &h.Note, &h.At); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ListSubmissions returns all work on an assignment, keyed by scholar.
func (s *Store) ListSubmissions(assignmentID int64) (map[int64]*Submission, error) {
	rows, err := s.db.Query(`SELECT `+submissionCols+` FROM submissions WHERE assignment_id = ?`, assignmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*Submission{}
	for rows.Next() {
		w, err := scanSubmission(rows)
		if err != nil {
			return nil, err
		}
		out[w.ScholarID] = w
	}
	return out, rows.Err()
}

// SubmissionsFor returns a scholar's work on several assignments, keyed by
// assignment.
func (s *Store) SubmissionsFor(scholarID int64, assignmentIDs []int64) (map[int64]*Submission, error) {
	out := map[int64]*Submission{}
	if len(assignmentIDs) == 0 {
		return out, nil
	}
	marks := make([]string, len(assignmentIDs))
	args := []any{scholarID}
	for i, id := range assignmentIDs {
		marks[i] = "?"
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT `+submissionCols+` FROM submissions WHERE scholar_id = ? AND assignment_id IN (`+
		strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		w, err := scanSubmission(rows)
		if err != nil {
			return nil, err
		}
		out[w.AssignmentID] = w
	}
	return out, rows.Err()
}

// StatusCounts counts work on each assignment by status, keyed by assignment.
func (s *Store) StatusCounts(assignmentIDs []int64) (map[int64]map[string]int, error) {
	out := map[int64]map[string]int{}
	if len(assignmentIDs) == 0 {
		return out, nil
	}
	marks := make([]string, len(assignmentIDs))
	args := make([]any, len(assignmentIDs))
	for i, id := range assignmentIDs {
		marks[i], args[i] = "?", id
		out[id] = map[string]int{}
	}
	rows, err := s.db.Query(`SELECT s.assignment_id, s.status, COUNT(*) FROM submissions s JOIN users u ON u.id = s.scholar_id
		WHERE u.active = 1 AND s.assignment_id IN (`+strings.Join(marks, ",")+`) GROUP BY s.assignment_id, s.status`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var status string
		var n int
		if err := rows.Scan(&id, &status, &n); err != nil {
			return nil, err
		}
		out[id][status] = n
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- files

// File is an uploaded file.
type File struct {
	ID          int64
	OwnerKind   string // "assignment" or "submission"
	OwnerID     int64
	Name        string
	Size        int64
	ContentType string
	Stored      string // path under the files folder
	UploadedBy  int64
	CreatedAt   int64
}

// Owner kinds for files.
const (
	FileForAssignment = "assignment"
	FileForSubmission = "submission"
	FileForMessage    = "message" // attached to a chat message
)

const fileCols = `id, owner_kind, owner_id, name, size, content_type, stored, COALESCE(uploaded_by, 0), created_at`

func scanFile(row interface{ Scan(...any) error }) (*File, error) {
	f := &File{}
	err := row.Scan(&f.ID, &f.OwnerKind, &f.OwnerID, &f.Name, &f.Size, &f.ContentType, &f.Stored, &f.UploadedBy, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return f, err
}

// AddFile records an uploaded file and sets f.ID.
func (s *Store) AddFile(f *File) error {
	f.CreatedAt = s.now()
	res, err := s.db.Exec(`INSERT INTO files (owner_kind, owner_id, name, size, content_type, stored, uploaded_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, f.OwnerKind, f.OwnerID, f.Name, f.Size, f.ContentType, f.Stored, nullID(f.UploadedBy), f.CreatedAt)
	if err != nil {
		return err
	}
	f.ID, err = res.LastInsertId()
	return err
}

// GetFile returns one file's record.
func (s *Store) GetFile(id int64) (*File, error) {
	return scanFile(s.db.QueryRow(`SELECT `+fileCols+` FROM files WHERE id = ?`, id))
}

// ListFiles returns the files attached to something, oldest first.
func (s *Store) ListFiles(kind string, ownerID int64) ([]*File, error) {
	rows, err := s.db.Query(`SELECT `+fileCols+` FROM files WHERE owner_kind = ? AND owner_id = ? ORDER BY id`, kind, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*File
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// FilesFor returns the files attached to several owners, keyed by owner.
func (s *Store) FilesFor(kind string, ownerIDs []int64) (map[int64][]*File, error) {
	out := map[int64][]*File{}
	if len(ownerIDs) == 0 {
		return out, nil
	}
	marks, args := idList(ownerIDs)
	rows, err := s.db.Query(`SELECT `+fileCols+` FROM files WHERE owner_kind = ? AND owner_id IN (`+marks+`) ORDER BY id`,
		append([]any{kind}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out[f.OwnerID] = append(out[f.OwnerID], f)
	}
	return out, rows.Err()
}

// DeleteFile removes a file's record and returns where it was stored.
func (s *Store) DeleteFile(id int64) (string, error) {
	f, err := s.GetFile(id)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(`DELETE FROM files WHERE id = ?`, id)
	return f.Stored, err
}

// OrphanFiles removes the records of files whose assignment or work is gone
// (deleted with a class or assignment), returning where they were stored.
func (s *Store) OrphanFiles() ([]string, error) {
	rows, err := s.db.Query(`SELECT id, stored FROM files f WHERE
		(f.owner_kind = 'assignment' AND NOT EXISTS (SELECT 1 FROM assignments a WHERE a.id = f.owner_id)) OR
		(f.owner_kind = 'submission' AND NOT EXISTS (SELECT 1 FROM submissions w WHERE w.id = f.owner_id)) OR
		(f.owner_kind = 'message' AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.id = f.owner_id AND m.deleted_at = 0))`)
	if err != nil {
		return nil, err
	}
	var ids []int64
	var paths []string
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
		paths = append(paths, p)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.db.Exec(`DELETE FROM files WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

// FilesSize is the total size of all uploaded files.
func (s *Store) FilesSize() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM files`).Scan(&n)
	return n, err
}
