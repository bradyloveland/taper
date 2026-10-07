package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bradyloveland/taper/internal/store"
	"github.com/bradyloveland/taper/internal/version"
)

// pageData is what every page template gets. D holds the page's own data.
type pageData struct {
	Title     string
	Section   string // highlights the menu item
	AppName   string // the school's name, or Taper before setup
	ShortName string // for phone home screens
	School    string
	Version   string
	Repo      string
	CSRF      string
	Path      string // the page's own address, for "Report a problem"
	User      *store.User
	Flash     *flash
	D         any
}

func (s *Server) templateFuncs() template.FuncMap {
	return template.FuncMap{
		"asset":     s.assets.url,
		"roleLabel": store.RoleLabel,
		"lower":     strings.ToLower,
		"ago":       func(unix int64) string { return ago(time.Unix(unix, 0), time.Now()) },
		"date":      func(unix int64) string { return time.Unix(unix, 0).Format("January 2, 2006") },
		"device":    device,
	}
}

// parseTemplates builds one template set per page, each with the layout.
func (s *Server) parseTemplates(fsys fs.FS) (map[string]*template.Template, error) {
	layout, err := template.New("layout.html").Funcs(s.templateFuncs()).ParseFS(fsys, "templates/layout.html")
	if err != nil {
		return nil, err
	}
	names, err := fs.Glob(fsys, "templates/*.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, n := range names {
		name := strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".html")
		if name == "layout" {
			continue
		}
		var t *template.Template
		if name == "offline" { // standalone, without the layout
			t, err = template.New("offline.html").Funcs(s.templateFuncs()).ParseFS(fsys, n)
		} else {
			t, err = template.Must(layout.Clone()).ParseFS(fsys, n)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		pages[name] = t
	}
	return pages, nil
}

func (s *Server) appNames() (name, short string) {
	name = s.store.SchoolName()
	if name == "" {
		name = version.Name
	}
	short = name
	if utf8.RuneCountInString(short) > 15 {
		short = version.Name
	}
	return name, short
}

// render writes a page with the layout.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page, title, section string, d any) {
	t, ok := s.pages[page]
	if !ok {
		panic("no template " + page)
	}
	name, short := s.appNames()
	pd := &pageData{Title: title, Section: section, AppName: name, ShortName: short, School: s.store.SchoolName(),
		Version: version.Version, Repo: version.Repo, Path: r.URL.Path, Flash: s.takeFlash(w, r), D: d}
	if ri := current(r); ri.user != nil {
		pd.User, pd.CSRF = ri.user, ri.sess.CSRF
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", pd); err != nil {
		s.logError(r, "rendering "+page, err)
		http.Error(w, "Something went wrong showing this page. Report a problem if it keeps happening.", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

type errorData struct{ Heading, Message string }

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	s.render(w, r, status, "error", heading, "", errorData{heading, message})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.renderError(w, r, http.StatusNotFound, "Page not found", "There's nothing at this address. It may have moved, or the link may be wrong.")
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	s.logError(r, msg, err)
	s.renderError(w, r, http.StatusInternalServerError, "Something went wrong",
		"The server couldn't finish that. Try again, and report a problem if it keeps happening.")
}

// ------------------------------------------------------------------ flash

const flashCookie = "taper_flash"

// flash is a one-time message shown on the next page.
type flash struct {
	Kind    string `json:"k"` // "ok" or "error"
	Message string `json:"m"`
}

func (s *Server) setFlash(w http.ResponseWriter, r *http.Request, kind, message string) {
	raw, _ := json.Marshal(flash{kind, message})
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: base64.RawURLEncoding.EncodeToString(raw), Path: "/",
		MaxAge: 120, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
}

func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	var f flash
	if json.Unmarshal(raw, &f) != nil || f.Message == "" || (f.Kind != "ok" && f.Kind != "error") {
		return nil
	}
	return &f
}

// redirect sends the browser to target with a message to show there.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, target, message string) {
	if message != "" {
		s.setFlash(w, r, "ok", message)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ----------------------------------------------------------------- helpers

// ago describes t relative to now in plain words.
func ago(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	}
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.AddDate(0, 0, -1).Date()
	if y1 == y2 && m1 == m2 && d1 == d2 {
		return "yesterday"
	}
	if t.Year() == now.Year() {
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// device names a browser from its User-Agent, like "Firefox on Windows".
func device(ua string) string {
	browser := ""
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Firefox/") || strings.Contains(ua, "FxiOS/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	system := ""
	switch {
	case strings.Contains(ua, "iPhone"):
		system = "iPhone"
	case strings.Contains(ua, "iPad"):
		system = "iPad"
	case strings.Contains(ua, "Android"):
		system = "Android"
	case strings.Contains(ua, "CrOS"):
		system = "Chromebook"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		system = "Mac"
	case strings.Contains(ua, "Windows"):
		system = "Windows"
	case strings.Contains(ua, "Linux"):
		system = "Linux"
	}
	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case system != "":
		return system
	}
	return "Unknown device"
}
