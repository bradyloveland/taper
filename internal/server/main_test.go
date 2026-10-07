package server

import (
	"html"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/config"
	"github.com/bradyloveland/taper/internal/store"
	"github.com/bradyloveland/taper/web"
)

func TestMain(m *testing.M) {
	auth.Iterations = 1000 // fast hashing for tests
	os.Exit(m.Run())
}

// env is a running server with a temporary database.
type env struct {
	t     *testing.T
	srv   *Server
	ts    *httptest.Server
	store *store.Store
	cfg   *config.Config
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith lets a test adjust the options; the program folder is
// cfg.AppDir, a temporary folder.
func newEnvWith(t *testing.T, adjust func(*Options)) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "taper.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	app := filepath.Join(dir, "opt")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Mode: config.ModeHTTP, Port: 8088, DataDir: dir, AppDir: app}
	opts := Options{Config: cfg, Store: st, Supervised: func() bool { return false }}
	if adjust != nil {
		adjust(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return &env{t: t, srv: srv, ts: ts, store: st, cfg: cfg}
}

// newEnvReady is a server that's been set up, with an admin "admin" whose
// password is "admin-password".
func newEnvReady(t *testing.T) *env { return newEnvReadyWith(t, nil) }

func newEnvReadyWith(t *testing.T, adjust func(*Options)) *env {
	e := newEnvWith(t, adjust)
	e.addUser("admin", "Ada Admin", store.RoleAdmin, "admin-password", false)
	if err := e.store.SetSetting("school_name", "Liberty Commonwealth"); err != nil {
		t.Fatal(err)
	}
	e.srv.setupDone.Store(true)
	return e
}

func (e *env) addUser(username, name, role, password string, mustChange bool) *store.User {
	e.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		e.t.Fatal(err)
	}
	u := &store.User{Username: username, DisplayName: name, Role: role, PasswordHash: hash, Active: true, MustChangePassword: mustChange}
	if err := e.store.CreateUser(u); err != nil {
		e.t.Fatal(err)
	}
	return u
}

// browser is a client with its own cookies that doesn't follow redirects.
type browser struct {
	e      *env
	base   string // the server's address; e.ts.URL by default
	c      *http.Client
	last   string // body of the last response
	header http.Header
}

func (e *env) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{e: e, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

type resp struct {
	Status   int
	Body     string
	Location string
	Header   http.Header
}

func (b *browser) do(req *http.Request) *resp {
	b.e.t.Helper()
	for k, v := range b.header {
		req.Header[k] = v
	}
	res, err := b.c.Do(req)
	if err != nil {
		b.e.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	b.last = string(body)
	return &resp{Status: res.StatusCode, Body: string(body), Location: res.Header.Get("Location"), Header: res.Header}
}

func (b *browser) get(path string) *resp {
	b.e.t.Helper()
	req, _ := http.NewRequest("GET", b.url()+path, nil)
	return b.do(req)
}

func (b *browser) post(path string, form url.Values) *resp {
	b.e.t.Helper()
	req, _ := http.NewRequest("POST", b.url()+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return b.do(req)
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (b *browser) url() string {
	if b.base != "" {
		return b.base
	}
	return b.e.ts.URL
}

// csrf returns the token from the last page fetched, or fetches /account.
func (b *browser) csrf() string {
	b.e.t.Helper()
	if m := csrfRE.FindStringSubmatch(b.last); m != nil {
		return html.UnescapeString(m[1])
	}
	b.get("/account")
	m := csrfRE.FindStringSubmatch(b.last)
	if m == nil {
		b.e.t.Fatalf("no CSRF token on the page:\n%s", b.last)
	}
	return html.UnescapeString(m[1])
}

// postForm posts with the CSRF token added.
func (b *browser) postForm(path string, form url.Values) *resp {
	b.e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", b.csrf())
	return b.post(path, form)
}

func (b *browser) login(username, password string) *resp {
	b.e.t.Helper()
	return b.post("/login", url.Values{"username": {username}, "password": {password}})
}

func (e *env) signedIn(username, password string) *browser {
	e.t.Helper()
	b := e.browser()
	if r := b.login(username, password); r.Status != http.StatusSeeOther {
		e.t.Fatalf("login as %s: status %d\n%s", username, r.Status, r.Body)
	}
	return b
}

func expect(t *testing.T, r *resp, status int, contains ...string) {
	t.Helper()
	if r.Status != status {
		t.Fatalf("status %d, want %d\n%s", r.Status, status, r.Body)
	}
	for _, c := range contains {
		if !strings.Contains(r.Body, c) && !strings.Contains(html.UnescapeString(r.Body), c) {
			t.Fatalf("response lacks %q:\n%s", c, r.Body)
		}
	}
}

func expectRedirect(t *testing.T, r *resp, location string) {
	t.Helper()
	if r.Status != http.StatusSeeOther && r.Status != http.StatusMovedPermanently && r.Status != http.StatusFound {
		t.Fatalf("status %d, want a redirect to %s\n%s", r.Status, location, r.Body)
	}
	if r.Location != location {
		t.Fatalf("redirect to %q, want %q", r.Location, location)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func templatesSource(name string) (string, error) {
	b, err := fs.ReadFile(web.Templates, "templates/"+name+".html")
	return string(b), err
}

func mustProxies(t *testing.T, list string) []netip.Prefix {
	t.Helper()
	c, err := config.Parse(func(k string) string {
		if k == "TAPER_TRUSTED_PROXIES" {
			return list
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return c.TrustedProxies
}
