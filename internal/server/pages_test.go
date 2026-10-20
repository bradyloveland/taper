package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/taper/docs"
	"github.com/bradyloveland/taper/internal/config"
	"github.com/bradyloveland/taper/internal/store"
)

func TestAccount(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("sam", "Sam Scholar", store.RoleScholar, "scholar-password", false)
	phone := e.signedIn("sam", "scholar-password")
	b := e.signedIn("sam", "scholar-password")
	expect(t, b.get("/account"), http.StatusOK, "My account", "Where you're signed in", "This device", "Sign out everywhere else")

	expectRedirect(t, b.postForm("/account/profile", url.Values{"display_name": {"Sam Stone"}, "email": {"sam@example.org"}}), "/account")
	expect(t, b.get("/account"), http.StatusOK, "Your profile is saved", `value="Sam Stone"`)
	expect(t, b.postForm("/account/profile", url.Values{"display_name": {""}}), http.StatusUnprocessableEntity, "Enter a name")
	// Scholars can't change their own role or username through the profile form.
	b.postForm("/account/profile", url.Values{"display_name": {"Sam Stone"}, "email": {"sam@example.org"}, "role": {"admin"}, "username": {"root"}})
	u, _ := e.store.GetUserByUsername("sam")
	if u.Role != store.RoleScholar || u.Username != "sam" || u.Email != "sam@example.org" {
		t.Fatalf("profile form changed the wrong things: %+v", u)
	}

	expect(t, b.postForm("/account/password", url.Values{"current": {"nope"}, "password": {"new password 1"}, "confirm": {"new password 1"}}), http.StatusUnprocessableEntity, "current password isn't right")
	expect(t, b.postForm("/account/password", url.Values{"current": {"scholar-password"}, "password": {"new password 1"}, "confirm": {"new password 2"}}), http.StatusUnprocessableEntity, "don't match")
	expectRedirect(t, b.postForm("/account/password", url.Values{"current": {"scholar-password"}, "password": {"new password 1"}, "confirm": {"new password 1"}}), "/account")
	expect(t, b.get("/account"), http.StatusOK, "Your password is changed")
	expectRedirect(t, phone.get("/"), "/login") // other devices signed out
	expectRedirect(t, e.browser().login("sam", "new password 1"), "/")

	// Sign out everywhere else.
	other := e.signedIn("sam", "new password 1")
	expectRedirect(t, b.postForm("/account/sessions/revoke", nil), "/account")
	expectRedirect(t, other.get("/"), "/login")
	expect(t, b.get("/"), http.StatusOK)
}

func TestHomeAndSettings(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	b := e.signedIn("admin", "admin-password")
	home := b.get("/")
	expect(t, home, http.StatusOK, "Getting started", "Liberty Commonwealth", `class="menu"`, `href="/admin/settings"`, `href="/guide"`, `action="/logout"`)
	if strings.Contains(home.Body, `class="admin-tabs"`) {
		t.Fatal("the admin tabs belong on the admin pages only")
	}
	for _, p := range []string{"/admin/people", "/admin/updates", "/admin/network", "/admin/email", "/admin/reports"} {
		expect(t, b.get(p), http.StatusOK, `class="admin-tabs"`, `href="`+p+`" aria-current="page"`)
	}
	expect(t, b.get("/admin/settings"), http.StatusOK, `class="admin-tabs"`, "This server", "Plain HTTP", "schema 13", "Download database and files", "Problem reports")
	expect(t, b.postForm("/admin/settings", url.Values{"school": {"   "}}), http.StatusUnprocessableEntity, "Enter a school name")
	expectRedirect(t, b.postForm("/admin/settings", url.Values{"school": {"Freedom   Academy"}}), "/admin/settings")
	expect(t, b.get("/admin/settings"), http.StatusOK, "Settings saved", "Freedom Academy")
	if e.store.SchoolName() != "Freedom Academy" {
		t.Fatal("school name not saved")
	}
	m := e.signedIn("mia", "mentor-password")
	expect(t, m.get("/"), http.StatusOK, ", Mia</h1>", "<p class=\"muted\">Freedom Academy</p>", "Your classes", `href="/account"`)
	if strings.Contains(m.last, `href="/admin/settings"`) {
		t.Fatal("mentors don't get Settings in their menu")
	}
	expect(t, m.get("/nope"), http.StatusNotFound, "Page not found")
}

func TestGuide(t *testing.T) {
	e := newEnvReady(t)
	b := e.signedIn("admin", "admin-password")
	expectRedirect(t, e.browser().get("/guide"), "/login?next=%2Fguide")
	r := b.get("/guide")
	expect(t, r, http.StatusOK, "Taper guide", "Light your taper at mine", `href="/guide/getting-started"`)
	if regexp.MustCompile(`href="[a-z0-9-]+\.md`).MatchString(r.Body) {
		t.Fatal("guide links to .md files instead of in-app pages")
	}
	// Every page renders, and every in-app guide link goes somewhere real.
	linkRE := regexp.MustCompile(`href="/guide/([a-z0-9-]+)(?:#([a-z0-9-]+))?`)
	for _, p := range e.srv.guide.pages {
		page := b.get("/guide/" + p.Slug)
		if p.Slug == "" {
			page = b.get("/guide")
		}
		expect(t, page, http.StatusOK, p.Title)
		for _, m := range linkRE.FindAllStringSubmatch(page.Body, -1) {
			target, ok := e.srv.guide.bySlug[m[1]]
			if !ok {
				t.Errorf("guide page %q links to missing page %q", p.Slug, m[1])
			} else if m[2] != "" && !strings.Contains(string(target.HTML), `id="`+m[2]+`"`) {
				t.Errorf("guide page %q links to missing heading %s#%s", p.Slug, m[1], m[2])
			}
		}
		if p.Title == "" || strings.HasSuffix(p.Title, ".md") {
			t.Errorf("guide page %q has no title", p.Slug)
		}
	}
	if len(e.srv.guide.pages) < 7 {
		t.Fatalf("only %d guide pages", len(e.srv.guide.pages))
	}
	// Every picture the guide shows is there, has alt text, and is served;
	// every picture in guide/images is used.
	imgRE := regexp.MustCompile(`<img src="/guide/images/([a-z0-9-]+\.png)" alt="([^"]*)"`)
	used := map[string]bool{}
	for _, p := range e.srv.guide.pages {
		for _, m := range imgRE.FindAllStringSubmatch(string(p.HTML), -1) {
			used[m[1]] = true
			if strings.TrimSpace(m[2]) == "" {
				t.Errorf("guide page %q: %s has no alt text", p.Slug, m[1])
			}
			if r := b.get("/guide/images/" + m[1]); r.Status != http.StatusOK || r.Header.Get("Content-Type") != "image/png" {
				t.Errorf("guide page %q: %s isn't served (%d)", p.Slug, m[1], r.Status)
			}
		}
	}
	files, _ := fs.Glob(docs.Guide, "guide/images/*.png")
	for _, f := range files {
		if !used[path.Base(f)] {
			t.Errorf("%s isn't used in the guide", f)
		}
	}
	if len(used) < 10 {
		t.Errorf("only %d pictures in the guide", len(used))
	}
	expect(t, b.get("/guide/images/nope.png"), http.StatusNotFound)
	expect(t, b.get("/guide/images/..%2fREADME.md"), http.StatusNotFound)
	expectRedirect(t, e.browser().get("/guide/images/chat.png"), "/login?next=%2Fguide%2Fimages%2Fchat.png")
	expect(t, b.get("/guide/nope"), http.StatusNotFound)
	expectRedirect(t, b.get("/guide/README"), "/guide")
	if got := guideLink("../plan.md#m1"); got != "https://github.com/bradyloveland/taper/blob/main/docs/plan.md#m1" {
		t.Fatalf("repo link = %q", got)
	}
	if guideLink("people.md#x") != "/guide/people#x" || guideLink("README.md") != "/guide" || guideLink("https://a.b/c.md") != "https://a.b/c.md" {
		t.Fatal("guide link rewriting wrong")
	}
}

func TestSecurityHeaders(t *testing.T) {
	e := newEnvReady(t)
	r := e.browser().get("/login")
	for h, want := range map[string]string{
		"Content-Security-Policy": "script-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "same-origin",
		"Cache-Control":           "no-store",
	} {
		if !strings.Contains(r.Header.Get(h), want) {
			t.Errorf("%s = %q, want %q", h, r.Header.Get(h), want)
		}
	}
	if strings.Contains(r.Header.Get("Content-Security-Policy"), "unsafe-inline") {
		t.Fatal("CSP allows inline code")
	}
	if r.Header.Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS over plain HTTP")
	}
	// No inline scripts, handlers or styles in any template.
	for name := range e.srv.pages {
		raw, _ := templatesSource(name)
		for _, bad := range []string{"<script>", " onclick=", " onload=", " style=\"", "<style"} {
			if strings.Contains(raw, bad) {
				t.Errorf("template %s contains %q, which the CSP blocks", name, bad)
			}
		}
	}
}

func TestPWA(t *testing.T) {
	e := newEnvReady(t)
	b := e.browser()

	r := b.get("/manifest.webmanifest")
	expect(t, r, http.StatusOK)
	var m struct {
		Name, ShortName, StartURL, Display string
		Icons                              []struct{ Src, Sizes, Purpose string }
	}
	raw := strings.NewReplacer(`"short_name"`, `"ShortName"`, `"start_url"`, `"StartURL"`).Replace(r.Body)
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "Liberty Commonwealth" || m.ShortName == "" || m.Display != "standalone" || m.StartURL != "/" {
		t.Fatalf("manifest: %+v", m)
	}
	var has192, has512, maskable bool
	for _, ic := range m.Icons {
		expect(t, b.get(ic.Src), http.StatusOK)
		has192 = has192 || ic.Sizes == "192x192"
		has512 = has512 || ic.Sizes == "512x512"
		maskable = maskable || ic.Purpose == "maskable"
	}
	if !has192 || !has512 || !maskable {
		t.Fatalf("manifest icons incomplete: %+v", m.Icons)
	}
	// iOS shows a letter instead of an SVG icon, so the manifest has PNGs only.
	for _, ic := range m.Icons {
		if !strings.Contains(ic.Src, ".png") {
			t.Errorf("manifest icon %s isn't a PNG", ic.Src)
		}
	}
	expect(t, b.get("/apple-touch-icon-precomposed.png"), http.StatusOK)

	sw := b.get("/sw.js")
	expect(t, sw, http.StatusOK, "taper-", "/static/app.css?v=")
	if strings.Contains(sw.Body, "__CACHE__") || strings.Contains(sw.Body, "__PRECACHE__") {
		t.Fatal("service worker placeholders not filled in")
	}
	if ct := sw.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Fatalf("sw.js content type %q", ct)
	}
	expect(t, b.get("/static/sw.js"), http.StatusNotFound)
	expect(t, b.get("/offline"), http.StatusOK, "You're offline")
	expect(t, b.get("/apple-touch-icon.png"), http.StatusOK)
	expect(t, b.get("/favicon.ico"), http.StatusOK)

	// Hashed asset URLs may be cached for good; others briefly.
	css := e.srv.assets.url("app.css")
	if r := b.get(css); !strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Fatalf("hashed asset cache: %q", r.Header.Get("Cache-Control"))
	}
	if r := b.get("/static/app.css"); strings.Contains(r.Header.Get("Cache-Control"), "immutable") {
		t.Fatal("unhashed asset marked immutable")
	}
	expect(t, b.get("/static/nope.css"), http.StatusNotFound)

	// The layout links everything a phone needs.
	page := b.get("/login")
	expect(t, page, http.StatusOK, `rel="manifest"`, `rel="apple-touch-icon" sizes="180x180" href="/static/icons/apple-touch-icon.png?v=`,
		`name="theme-color"`, `name="viewport"`)
}

func TestBehindProxy(t *testing.T) {
	e := newEnvReady(t)
	e.srv.cfg.TrustedProxies = mustProxies(t, "127.0.0.1")
	b := e.browser()
	b.header = http.Header{"X-Forwarded-Proto": {"https"}, "X-Forwarded-For": {"203.0.113.9, 127.0.0.1"}}
	r := b.login("admin", "admin-password")
	if c := r.Header.Get("Set-Cookie"); !strings.Contains(c, "Secure") {
		t.Fatalf("cookie should be Secure behind an HTTPS proxy: %s", c)
	}
	if r.Header.Get("Strict-Transport-Security") == "" {
		t.Fatal("HSTS missing behind an HTTPS proxy")
	}
	list, _ := e.store.UserSessions(1)
	if len(list) != 1 || list[0].IP != "203.0.113.9" {
		t.Fatalf("client address from X-Forwarded-For: %+v", list)
	}

	// An untrusted sender's headers are ignored.
	e.srv.cfg.TrustedProxies = nil
	b2 := e.browser()
	b2.header = b.header
	r = b2.login("admin", "admin-password")
	if strings.Contains(r.Header.Get("Set-Cookie"), "Secure") {
		t.Fatal("trusted X-Forwarded-Proto from an untrusted sender")
	}
}

func TestHelpers(t *testing.T) {
	now := time.Date(2026, 10, 6, 15, 0, 0, 0, time.Local)
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now", time.Minute: "1 minute ago", 5 * time.Minute: "5 minutes ago",
		3 * time.Hour: "3 hours ago", 20 * time.Hour: "20 hours ago", 30 * time.Hour: "yesterday",
		4 * 24 * time.Hour: "Oct 2", 400 * 24 * time.Hour: "Sep 1, 2025",
	} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
	for ua, want := range map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:130.0) Gecko/20100101 Firefox/130.0":                                    "Firefox on Windows",
		"Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/129.0 Mobile Safari/537.36":                                "Chrome on Android",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/129.0 Safari/537.36 Edg/129.0":             "Edge on Mac",
		"curl/8.0": "Unknown device",
	} {
		if got := device(ua); got != want {
			t.Errorf("device(%q) = %q, want %q", ua, got, want)
		}
	}
	if humanSize(500) != "500 bytes" || humanSize(1536) != "1.5 KB" || humanSize(10<<20) != "10 MB" {
		t.Fatalf("humanSize: %s %s %s", humanSize(500), humanSize(1536), humanSize(10<<20))
	}
	if greeting(time.Date(2026, 1, 1, 9, 0, 0, 0, time.Local)) != "Good morning" || greeting(time.Date(2026, 1, 1, 20, 0, 0, 0, time.Local)) != "Good evening" {
		t.Fatal("greeting")
	}
}

func TestBaseURL(t *testing.T) {
	e := newEnvReady(t)
	req := httptest.NewRequest("GET", "http://school.lan:8088/", nil)
	if got := e.srv.baseURL(req); got != "http://school.lan:8088" {
		t.Fatalf("baseURL = %q", got)
	}
	e.srv.cfg.Mode, e.srv.cfg.Domain = config.ModeHTTPS, "learn.example.org"
	if got := e.srv.baseURL(req); got != "https://learn.example.org" {
		t.Fatalf("baseURL = %q", got)
	}
}

func TestStaticCompression(t *testing.T) {
	e := newEnvReady(t)
	b := e.browser()
	css := e.srv.assets.url("app.css")
	want := e.srv.assets.files["app.css"]
	// Asking for gzip itself, the test's HTTP client leaves the body compressed.
	b.header = http.Header{"Accept-Encoding": {"gzip"}}
	r := b.get(css)
	if r.Header.Get("Content-Encoding") != "gzip" || r.Header.Get("Vary") != "Accept-Encoding" || len(r.Body) >= len(want)/2 {
		t.Fatalf("compressed: %v, %d bytes", r.Header, len(r.Body))
	}
	zr, err := gzip.NewReader(strings.NewReader(r.Body))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(zr); !bytes.Equal(got, want) {
		t.Fatal("the compressed stylesheet doesn't unpack to the original")
	}
	b.header = http.Header{"If-None-Match": {r.Header.Get("ETag")}, "Accept-Encoding": {"gzip"}}
	expect(t, b.get(css), http.StatusNotModified)
	b.header = http.Header{"Accept-Encoding": {"identity"}}
	if r := b.get(css); r.Header.Get("Content-Encoding") != "" || r.Body != string(want) {
		t.Fatal("without gzip, the plain file")
	}
	// Pages are never compressed (BREACH).
	b.header = http.Header{"Accept-Encoding": {"gzip"}}
	if r := b.get("/login"); r.Header.Get("Content-Encoding") != "" {
		t.Fatal("pages mustn't be compressed")
	}
}
