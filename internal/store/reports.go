package store

import (
	"database/sql"
	"errors"
)

// BugReport is a problem someone reported from inside Taper.
type BugReport struct {
	ID          int64
	UserID      int64 // 0 if the account was removed
	Reporter    string
	Title       string
	Body        string // the issue text, as sent to GitHub
	Page        string
	IssueURL    string // "" until it's on GitHub
	IssueNumber int
	Error       string // why sending it to GitHub failed, if it did
	CreatedAt   int64
}

// CreateBugReport saves a report and sets r.ID.
func (s *Store) CreateBugReport(r *BugReport) error {
	r.CreatedAt = s.now()
	var uid any
	if r.UserID != 0 {
		uid = r.UserID
	}
	res, err := s.db.Exec(`INSERT INTO bug_reports (user_id, title, body, page, issue_url, issue_number, error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, uid, r.Title, r.Body, r.Page, r.IssueURL, r.IssueNumber, r.Error, r.CreatedAt)
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	return err
}

// SetBugReportIssue records the GitHub issue a report became, or why it couldn't be sent.
func (s *Store) SetBugReportIssue(id int64, url string, number int, errMsg string) error {
	_, err := s.db.Exec(`UPDATE bug_reports SET issue_url = ?, issue_number = ?, error = ? WHERE id = ?`, url, number, errMsg, id)
	return err
}

const reportCols = `r.id, COALESCE(r.user_id, 0), COALESCE(u.display_name, ''), r.title, r.body, r.page,
	r.issue_url, r.issue_number, r.error, r.created_at`

func scanReport(row interface{ Scan(...any) error }) (*BugReport, error) {
	r := &BugReport{}
	err := row.Scan(&r.ID, &r.UserID, &r.Reporter, &r.Title, &r.Body, &r.Page, &r.IssueURL, &r.IssueNumber, &r.Error, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// GetBugReport returns one report.
func (s *Store) GetBugReport(id int64) (*BugReport, error) {
	return scanReport(s.db.QueryRow(`SELECT `+reportCols+` FROM bug_reports r LEFT JOIN users u ON u.id = r.user_id
		WHERE r.id = ?`, id))
}

// ListBugReports returns the newest reports first.
func (s *Store) ListBugReports(limit int) ([]*BugReport, error) {
	rows, err := s.db.Query(`SELECT `+reportCols+` FROM bug_reports r LEFT JOIN users u ON u.id = r.user_id
		ORDER BY r.created_at DESC, r.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*BugReport
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UnsentBugReports counts reports that aren't on GitHub.
func (s *Store) UnsentBugReports() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM bug_reports WHERE issue_url = ''`).Scan(&n)
	return n, err
}
