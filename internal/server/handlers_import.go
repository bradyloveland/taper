package server

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/store"
)

// maxImportRows keeps an import to a size one page can show.
const maxImportRows = 1000

// importRow is one person from a spreadsheet.
type importRow struct {
	Line      int
	Name      string
	Username  string
	Email     string
	Role      string
	Classes   []*store.Class
	Generated bool // the username was made from the name
	Problems  []string
}

func (r *importRow) OK() bool { return len(r.Problems) == 0 }

// ClassNames joins the row's class names for display.
func (r *importRow) ClassNames() string {
	names := make([]string, len(r.Classes))
	for i, c := range r.Classes {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

var importHeaders = map[string]string{
	"name": "name", "full name": "name", "display name": "name", "scholar": "name", "student": "name",
	"first name": "first", "first": "first", "given name": "first",
	"last name": "last", "last": "last", "surname": "last", "family name": "last",
	"username": "username", "user name": "username", "login": "username", "user": "username",
	"email": "email", "e-mail": "email", "email address": "email",
	"role": "role", "type": "role",
	"class": "classes", "classes": "classes",
}

var importRoles = map[string]string{
	"": store.RoleScholar, "scholar": store.RoleScholar, "student": store.RoleScholar,
	"mentor": store.RoleMentor, "teacher": store.RoleMentor,
	"admin": store.RoleAdmin, "administrator": store.RoleAdmin,
}

// parseImport reads CSV (or tab-separated text pasted from a spreadsheet).
// It returns the rows, or a message if the text can't be read at all.
func (s *Server) parseImport(text string) ([]*importRow, string) {
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	if strings.TrimSpace(text) == "" {
		return nil, "Choose a spreadsheet file, or paste the list."
	}
	first, _, _ := strings.Cut(text, "\n")
	cr := csv.NewReader(strings.NewReader(text))
	switch {
	case strings.Contains(first, "\t"):
		cr.Comma = '\t'
	case strings.Count(first, ";") > strings.Count(first, ","):
		cr.Comma = ';'
	}
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	records, err := cr.ReadAll()
	if err != nil {
		var pe *csv.ParseError
		if errors.As(err, &pe) {
			return nil, fmt.Sprintf("Line %d of the spreadsheet couldn't be read (check its quote marks).", pe.Line)
		}
		return nil, "The spreadsheet couldn't be read. Save it as CSV and try again."
	}
	cols := map[string]int{}
	for i, h := range records[0] {
		if key, ok := importHeaders[strings.ToLower(strings.TrimSpace(h))]; ok {
			if _, dup := cols[key]; !dup {
				cols[key] = i
			}
		}
	}
	_, hasName := cols["name"]
	_, hasFirst := cols["first"]
	if !hasName && !hasFirst {
		return nil, "The first row should name the columns: at least \"Name\" (or \"First name\" and \"Last name\"), and optionally \"Username\", \"Email\", \"Role\" and \"Classes\"."
	}
	if len(records)-1 > maxImportRows {
		return nil, fmt.Sprintf("That's more than %d people. Split the spreadsheet into smaller ones.", maxImportRows)
	}
	classes, _ := s.store.ListClasses(store.ClassFilter{})
	byName := map[string]*store.Class{}
	for _, c := range classes {
		byName[strings.ToLower(c.Name)] = c
	}
	get := func(rec []string, key string) string {
		i, ok := cols[key]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}
	taken := map[string]bool{}
	var rows []*importRow
	for n, rec := range records[1:] {
		empty := true
		for _, f := range rec {
			if strings.TrimSpace(f) != "" {
				empty = false
			}
		}
		if empty {
			continue
		}
		row := &importRow{Line: n + 2}
		name := get(rec, "name")
		if name == "" {
			name = strings.TrimSpace(get(rec, "first") + " " + get(rec, "last"))
		}
		var msg string
		if row.Name, msg = cleanName(name, "name"); msg != "" {
			row.Name = name
			row.Problems = append(row.Problems, "No name.")
		}
		role, ok := importRoles[strings.ToLower(get(rec, "role"))]
		if !ok {
			row.Problems = append(row.Problems, "The role should be scholar, mentor or admin.")
			role = store.RoleScholar
		}
		row.Role = role
		if row.Email, msg = cleanEmail(get(rec, "email")); msg != "" {
			row.Email = get(rec, "email")
			row.Problems = append(row.Problems, "The email address doesn't look right.")
		}
		if u := get(rec, "username"); u != "" {
			if row.Username, msg = cleanUsername(u); msg != "" {
				row.Username = u
				row.Problems = append(row.Problems, "The username can only have letters, numbers, dots, dashes and underscores.")
			} else if taken[row.Username] {
				row.Problems = append(row.Problems, "This username is in the list twice.")
			} else if _, err := s.store.GetUserByUsername(row.Username); err == nil {
				row.Problems = append(row.Problems, "Someone already has this username.")
			}
		} else if row.Name != "" {
			row.Username, row.Generated = s.uniqueUsername(row.Name, taken), true
		}
		taken[row.Username] = true
		for _, cn := range strings.FieldsFunc(get(rec, "classes"), func(r rune) bool { return r == ';' || r == '|' }) {
			cn = strings.TrimSpace(cn)
			if cn == "" {
				continue
			}
			if c, ok := byName[strings.ToLower(cn)]; ok {
				row.Classes = append(row.Classes, c)
			} else {
				row.Problems = append(row.Problems, fmt.Sprintf("There's no current class called %q.", cn))
			}
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, "There's no one in the list, only the row of column names."
	}
	return rows, ""
}

// usernameFrom makes a username like "ann.adams" from a name.
func usernameFrom(name string) string {
	var b strings.Builder
	dot := false
	for _, r := range strings.ToLower(name) {
		if t, ok := transliterate[r]; ok {
			b.WriteString(t)
			dot = false
			continue
		}
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dot = false
		} else if !dot && b.Len() > 0 {
			b.WriteByte('.')
			dot = true
		}
	}
	u := strings.Trim(b.String(), ".")
	if len(u) > 36 {
		u = strings.Trim(u[:36], ".")
	}
	if len(u) < 2 {
		u = "user"
	}
	return u
}

var transliterate = map[rune]string{
	'á': "a", 'à': "a", 'â': "a", 'ä': "a", 'ã': "a", 'å': "a", 'æ': "ae", 'ç': "c", 'é': "e", 'è': "e", 'ê': "e", 'ë': "e",
	'í': "i", 'ì': "i", 'î': "i", 'ï': "i", 'ñ': "n", 'ó': "o", 'ò': "o", 'ô': "o", 'ö': "o", 'õ': "o", 'ø': "o", 'œ': "oe",
	'ú': "u", 'ù': "u", 'û': "u", 'ü': "u", 'ý': "y", 'ÿ': "y", 'ß': "ss", 'ł': "l", 'š': "s", 'ž': "z", 'č': "c", 'ř': "r",
}

// uniqueUsername adds a number if the username is taken.
func (s *Server) uniqueUsername(name string, taken map[string]bool) string {
	base := usernameFrom(name)
	for i := 1; ; i++ {
		u := base
		if i > 1 {
			u = fmt.Sprintf("%s%d", base, i)
		}
		if taken[u] {
			continue
		}
		if _, err := s.store.GetUserByUsername(u); err == nil {
			continue
		}
		return u
	}
}

type importData struct {
	Text    string
	Rows    []*importRow
	Good    int
	Bad     int
	Error   string
	Created []importedPerson
	CSV     string // name,username,password for downloading
	URL     string
}

type importedPerson struct {
	Name, Username, Role, Password, Classes string
}

func (s *Server) handleImportForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "people-import", "Import people", "people", importData{})
}

// importText reads the uploaded file or the pasted text.
func importText(r *http.Request) string {
	if f, _, err := r.FormFile("file"); err == nil {
		defer f.Close()
		raw, _ := io.ReadAll(io.LimitReader(f, maxForm))
		if len(bytes.TrimSpace(raw)) > 0 {
			return string(raw)
		}
	}
	return r.PostFormValue("text")
}

func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	text := importText(r)
	rows, msg := s.parseImport(text)
	d := importData{Text: text, Rows: rows, Error: msg}
	for _, row := range rows {
		if row.OK() {
			d.Good++
		} else {
			d.Bad++
		}
	}
	status := http.StatusOK
	if msg != "" {
		status = http.StatusUnprocessableEntity
	}
	s.render(w, r, status, "people-import", "Import people", "people", d)
}

func (s *Server) handleImportCreate(w http.ResponseWriter, r *http.Request) {
	rows, msg := s.parseImport(r.PostFormValue("text"))
	if msg != "" {
		s.render(w, r, http.StatusUnprocessableEntity, "people-import", "Import people", "people", importData{Error: msg})
		return
	}
	d := importData{URL: s.baseURL(r)}
	var out bytes.Buffer
	cw := csv.NewWriter(&out)
	_ = cw.Write([]string{"Name", "Username", "Temporary password", "Role", "Classes"})
	for _, row := range rows {
		if !row.OK() {
			continue
		}
		temp := auth.TempPassword()
		hash, err := auth.HashPassword(temp)
		if err != nil {
			s.serverError(w, r, "hashing password", err)
			return
		}
		u := &store.User{Username: row.Username, DisplayName: row.Name, Email: row.Email, Role: row.Role, PasswordHash: hash,
			Active: true, MustChangePassword: true}
		if err := s.store.CreateUser(u); errors.Is(err, store.ErrUsernameTaken) {
			continue // taken since the preview
		} else if err != nil {
			s.serverError(w, r, "creating person", err)
			return
		}
		classRole := store.ClassScholar
		if u.IsMentor() {
			classRole = store.ClassMentor
		}
		for _, c := range row.Classes {
			if _, err := s.store.AddMembers(c.ID, classRole, []int64{u.ID}); err != nil {
				s.serverError(w, r, "adding to class", err)
				return
			}
		}
		p := importedPerson{Name: u.DisplayName, Username: u.Username, Role: u.RoleLabel(), Password: temp, Classes: row.ClassNames()}
		d.Created = append(d.Created, p)
		_ = cw.Write([]string{p.Name, p.Username, p.Password, p.Role, p.Classes})
	}
	cw.Flush()
	d.CSV = out.String()
	slog.Info("people imported", "by", current(r).user.Username, "count", len(d.Created))
	s.render(w, r, http.StatusOK, "people-imported", "People imported", "people", d)
}

// handleImportCredentials sends back the list of temporary passwords as a
// CSV file. The list is only ever in the admin's browser; this just turns it
// into a download.
func (s *Server) handleImportCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="taper-new-accounts.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, "\ufeff"+r.PostFormValue("csv")) // the BOM makes Excel read it as UTF-8
}
