package server

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/mail/mailtest"
	"github.com/bradyloveland/taper/internal/store"
)

// withEmail sets up email through the settings page, using a test server.
func withEmail(t *testing.T, e *env) *mailtest.Server {
	t.Helper()
	srv := mailtest.New(t)
	srv.Password = "mail-pass"
	a := e.signedIn("admin", "admin-password")
	r := a.postForm("/admin/email", url.Values{"host": {srv.Host}, "port": {fmt.Sprint(srv.Port)}, "security": {"none"},
		"username": {"user"}, "password": {"mail-pass"}, "from": {"taper@school.example"}, "alerts": {"1"}})
	expectRedirect(t, r, "/admin/email")
	return srv
}

func waitMessages(t *testing.T, e *env, srv *mailtest.Server, n int) []mailtest.Message {
	t.Helper()
	e.srv.wg.Wait()
	got := srv.Messages()
	if len(got) != n {
		t.Fatalf("got %d emails, want %d: %+v", len(got), n, got)
	}
	return got
}

func TestEmailSettings(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	expect(t, e.signedIn("mia", "mentor-password").get("/admin/email"), http.StatusForbidden)
	a := e.signedIn("admin", "admin-password")
	expect(t, a.get("/admin/settings"), http.StatusOK, "Not set up")
	expect(t, a.get("/admin/email"), http.StatusOK, "Mail server (SMTP)")
	expect(t, a.postForm("/admin/email", url.Values{"host": {"smtp.example.org"}, "port": {"587"}, "security": {"starttls"}, "from": {"nope"}}),
		http.StatusUnprocessableEntity, "address emails come from")
	expect(t, a.postForm("/admin/email/test", url.Values{"to": {"x@example.org"}}), http.StatusUnprocessableEntity, "Save the email settings first")

	srv := withEmail(t, e)
	var raw string
	e.store.GetSetting(settingSMTPPassword, &raw)
	if raw == "" || strings.Contains(raw, "mail-pass") {
		t.Fatalf("the password should be stored encrypted: %q", raw)
	}
	page := a.get("/admin/email")
	expect(t, page, http.StatusOK, "Saved; leave empty to keep it")
	if strings.Contains(page.Body, "mail-pass") {
		t.Fatal("the password must never be shown")
	}
	expect(t, a.get("/admin/settings"), http.StatusOK, "Set up")

	expect(t, a.postForm("/admin/email/test", url.Values{"to": {"ada@example.org"}}), http.StatusOK, "Sent.")
	got := waitMessages(t, e, srv, 1)
	if got[0].To[0] != "ada@example.org" || got[0].Subject != "Test email from Taper" || got[0].User != "user" {
		t.Fatalf("test email: %+v", got[0])
	}
	// Saving again without a password keeps it.
	expectRedirect(t, a.postForm("/admin/email", url.Values{"host": {srv.Host}, "port": {fmt.Sprint(srv.Port)}, "security": {"none"},
		"username": {"user"}, "from": {"taper@school.example"}}), "/admin/email")
	expect(t, a.postForm("/admin/email/test", url.Values{"to": {"ada@example.org"}}), http.StatusOK, "Sent.")
	// A wrong password is reported.
	expectRedirect(t, a.postForm("/admin/email", url.Values{"host": {srv.Host}, "port": {fmt.Sprint(srv.Port)}, "security": {"none"},
		"username": {"user"}, "password": {"wrong"}, "from": {"taper@school.example"}}), "/admin/email")
	expect(t, a.postForm("/admin/email/test", url.Values{"to": {"ada@example.org"}}), http.StatusBadGateway, "It didn't work", "username and password")
	// Turning it off.
	expectRedirect(t, a.postForm("/admin/email", url.Values{"clear": {"1"}}), "/admin/email")
	if e.srv.emailReady() {
		t.Fatal("email should be off")
	}
}

var resetLinkRE = regexp.MustCompile(`(http://[^\s]+/reset/[A-Za-z0-9_-]+)`)

func TestForgotPassword(t *testing.T) {
	e := newEnvReady(t)
	sam := e.addUser("sam", "Sam Scholar", store.RoleScholar, "old-password", false)
	sam.Email = "sam@example.org"
	e.store.UpdateUser(sam)
	e.addUser("noemail", "No Email", store.RoleScholar, "pw-123456", false)

	// Without email set up, there's no reset link.
	expect(t, e.browser().get("/login"), http.StatusOK, "Ask a mentor or admin")
	expectRedirect(t, e.browser().get("/forgot"), "/login")

	srv := withEmail(t, e)
	b := e.browser()
	signedIn := e.signedIn("sam", "old-password")
	expect(t, b.get("/login"), http.StatusOK, `href="/forgot"`)
	expect(t, b.get("/forgot"), http.StatusOK, "Username or email address")
	// The same answer for an unknown account, one without an address, and a real one.
	for _, who := range []string{"nobody", "noemail", "SAM"} {
		expect(t, b.post("/forgot", url.Values{"who": {who}}), http.StatusOK, "If that account has an email address")
	}
	got := waitMessages(t, e, srv, 1)
	if got[0].To[0] != "sam@example.org" || got[0].Subject != "Reset your Taper password" || !strings.Contains(got[0].Body, "Hello Sam Scholar") {
		t.Fatalf("reset email: %+v", got[0])
	}
	m := resetLinkRE.FindStringSubmatch(got[0].Body)
	if m == nil {
		t.Fatalf("no link in:\n%s", got[0].Body)
	}
	link, _ := url.Parse(m[1])

	expect(t, b.get(link.Path), http.StatusOK, "Choose a new password", "Sam Scholar")
	expect(t, b.post(link.Path, url.Values{"password": {"short"}, "confirm": {"short"}}), http.StatusUnprocessableEntity, "at least 8")
	expectRedirect(t, b.post(link.Path, url.Values{"password": {"brand new pass"}, "confirm": {"brand new pass"}}), "/login")
	expect(t, b.get("/login"), http.StatusOK, "Your password is changed")
	expectRedirect(t, signedIn.get("/"), "/login") // signed out everywhere
	expect(t, e.browser().login("sam", "old-password"), http.StatusUnauthorized)
	expectRedirect(t, e.browser().login("sam", "brand new pass"), "/")
	// The link works once.
	expect(t, b.get(link.Path), http.StatusNotFound, "doesn't work")
	expect(t, b.post(link.Path, url.Values{"password": {"another pass 1"}, "confirm": {"another pass 1"}}), http.StatusNotFound)

	// By email address too, and limited per address.
	expect(t, b.post("/forgot", url.Values{"who": {"Sam@Example.org"}}), http.StatusOK, "If that account")
	waitMessages(t, e, srv, 2)
	b.post("/forgot", url.Values{"who": {"sam@example.org"}})
	b.post("/forgot", url.Values{"who": {"sam@example.org"}})
	expect(t, b.post("/forgot", url.Values{"who": {"sam@example.org"}}), http.StatusTooManyRequests, "try again in an hour")
}

func TestAdminNotices(t *testing.T) {
	e, _ := updateEnv(t)
	admin, _ := e.store.GetUserByUsername("admin")
	admin.Email = "ada@example.org"
	e.store.UpdateUser(admin)
	srv := withEmail(t, e)
	e.store.SetSetting(settingPublicURL, "https://learn.example.org")

	// A new version is announced once.
	b := e.signedIn("admin", "admin-password")
	b.postForm("/admin/updates/check", nil)
	e.srv.checkUpdateNotices()
	e.srv.checkUpdateNotices()
	got := waitMessages(t, e, srv, 1)
	if got[0].Subject != "Taper 99.0.0 is available" || !strings.Contains(got[0].Body, "https://learn.example.org/admin/updates") {
		t.Fatalf("update notice: %+v", got[0])
	}

	// A problem report.
	e.addUser("sam", "Sam Scholar", store.RoleScholar, "scholar-password", false)
	s := e.signedIn("sam", "scholar-password")
	s.postForm("/report", url.Values{"title": {"Broken thing"}, "what": {"It broke."}})
	got = waitMessages(t, e, srv, 2)
	if got[1].Subject != "Problem report: Broken thing" || !strings.Contains(got[1].Body, "Sam Scholar reported") {
		t.Fatalf("report notice: %+v", got[1])
	}

	// Turned off, nothing is sent.
	expectRedirect(t, b.postForm("/admin/email", url.Values{"host": {srv.Host}, "port": {fmt.Sprint(srv.Port)}, "security": {"none"},
		"username": {"user"}, "from": {"taper@school.example"}}), "/admin/email")
	s.postForm("/report", url.Values{"title": {"Another"}, "what": {"x"}})
	waitMessages(t, e, srv, 2)
}
