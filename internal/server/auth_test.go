package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/store"
)

func TestSetup(t *testing.T) {
	e := newEnv(t)
	b := e.browser()

	expectRedirect(t, b.get("/"), "/setup")
	expectRedirect(t, b.get("/login"), "/setup")
	expectRedirect(t, b.get("/admin/people"), "/setup")
	expect(t, b.get("/healthz"), http.StatusOK, "ok")
	expect(t, b.get("/setup"), http.StatusOK, "Set up Taper", "Light your taper at mine")
	code, err := EnsureSetupCode(e.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"code": {"WRONG-CODE"}, "school": {"Liberty Commonwealth"}, "display_name": {"Ada Admin"},
		"username": {"Ada"}, "password": {"a good password"}, "confirm": {"a good password"}}
	expect(t, b.post("/setup", form), http.StatusUnprocessableEntity, "setup code isn")

	form.Set("code", strings.ToLower(code))
	form.Set("confirm", "different")
	expect(t, b.post("/setup", form), http.StatusUnprocessableEntity, "don&#39;t match")
	form.Set("confirm", "a good password")
	form.Set("username", "bad name!")
	expect(t, b.post("/setup", form), http.StatusUnprocessableEntity, "Usernames are")
	form.Set("username", "Ada")

	r := b.post("/setup", form)
	expectRedirect(t, r, "/")
	expect(t, b.get("/"), http.StatusOK, "Ada Admin", "Liberty Commonwealth", "Taper is ready")
	if fileExists(SetupCodePath(e.cfg.DataDir)) {
		t.Fatal("the setup code should be removed after setup")
	}
	u, err := e.store.GetUserByUsername("ada")
	if err != nil || !u.IsAdmin() || u.Username != "ada" {
		t.Fatalf("admin not created as expected: %+v %v", u, err)
	}

	// Setup can't be run again, even with a code.
	expectRedirect(t, b.get("/setup"), "/login")
	expectRedirect(t, e.browser().post("/setup", form), "/login")
	if n, _ := e.store.CountUsers(); n != 1 {
		t.Fatalf("users = %d", n)
	}
}

func TestSetupThrottle(t *testing.T) {
	e := newEnv(t)
	b := e.browser()
	for i := 0; i < 50; i++ {
		b.post("/setup", url.Values{"code": {"NOPE"}})
	}
	expect(t, b.post("/setup", url.Values{"code": {"NOPE"}}), http.StatusTooManyRequests, "Too many tries")
}

func TestLoginLogout(t *testing.T) {
	e := newEnvReady(t)
	b := e.browser()
	expectRedirect(t, b.get("/"), "/login")
	expectRedirect(t, b.get("/account?tab=1"), "/login?next=%2Faccount%3Ftab%3D1")
	expect(t, b.get("/login"), http.StatusOK, "Sign in", "Light your taper at mine")

	expect(t, b.login("admin", "wrong"), http.StatusUnauthorized, "don&#39;t match")
	expect(t, b.login("nobody", "wrong"), http.StatusUnauthorized, "don&#39;t match")

	r := b.post("/login", url.Values{"username": {"ADMIN"}, "password": {"admin-password"}, "next": {"/account"}})
	expectRedirect(t, r, "/account")
	if c := r.Header.Get("Set-Cookie"); !strings.Contains(c, "HttpOnly") || !strings.Contains(c, "SameSite=Lax") || strings.Contains(c, "Max-Age") {
		t.Fatalf("session cookie flags wrong: %s", c)
	}
	expect(t, b.get("/"), http.StatusOK, "Ada Admin")
	expectRedirect(t, b.get("/login"), "/")
	u, _ := e.store.GetUserByUsername("admin")
	if u.LastLoginAt == 0 {
		t.Fatal("last sign-in not recorded")
	}

	// Signing out needs the CSRF token.
	expect(t, b.post("/logout", url.Values{}), http.StatusForbidden, "form expired")
	expectRedirect(t, b.postForm("/logout", nil), "/login")
	expect(t, b.get("/login"), http.StatusOK, "You&#39;re signed out")
	expectRedirect(t, b.get("/"), "/login")

	// "Keep me signed in" sets a lasting cookie.
	r = b.post("/login", url.Values{"username": {"admin"}, "password": {"admin-password"}, "remember": {"1"}})
	if c := r.Header.Get("Set-Cookie"); !strings.Contains(c, "Max-Age=2592000") {
		t.Fatalf("remembered cookie should last 30 days: %s", c)
	}
}

func TestLoginOpenRedirect(t *testing.T) {
	e := newEnvReady(t)
	for _, next := range []string{"//evil.example", "https://evil.example/", `/\evil.example`, "javascript:alert(1)"} {
		r := e.browser().post("/login", url.Values{"username": {"admin"}, "password": {"admin-password"}, "next": {next}})
		expectRedirect(t, r, "/")
	}
}

func TestLoginThrottle(t *testing.T) {
	e := newEnvReady(t)
	b := e.browser()
	for i := 0; i < 5; i++ {
		expect(t, b.login("admin", "wrong"), http.StatusUnauthorized)
	}
	// Even the right password waits once the account is throttled.
	expect(t, b.login("admin", "admin-password"), http.StatusTooManyRequests, "Too many tries")
	// Other accounts are unaffected.
	e.addUser("sam", "Sam Scholar", store.RoleScholar, "sam-password", false)
	expectRedirect(t, b.login("sam", "sam-password"), "/")
}

func TestDeactivatedLogin(t *testing.T) {
	e := newEnvReady(t)
	u := e.addUser("sam", "Sam Scholar", store.RoleScholar, "sam-password", false)
	b := e.signedIn("sam", "sam-password")
	u.Active = false
	if err := e.store.UpdateUser(u); err != nil {
		t.Fatal(err)
	}
	expectRedirect(t, b.get("/"), "/login")
	expect(t, b.login("sam", "wrong"), http.StatusUnauthorized, "don&#39;t match")
	expect(t, b.login("sam", "sam-password"), http.StatusForbidden, "turned off")
}

func TestTemporaryPassword(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("sam", "Sam Scholar", store.RoleScholar, "temp-pass-1", true)
	b := e.browser()
	expectRedirect(t, b.login("sam", "temp-pass-1"), "/password")
	// Every other page sends them back until they choose a password.
	expectRedirect(t, b.get("/"), "/password")
	expectRedirect(t, b.get("/guide"), "/password")
	expect(t, b.get("/password"), http.StatusOK, "Choose a new password")
	expect(t, b.postForm("/password", url.Values{"password": {"short"}, "confirm": {"short"}}), http.StatusUnprocessableEntity, "at least 8")
	expect(t, b.postForm("/password", url.Values{"password": {"temp-pass-1"}, "confirm": {"temp-pass-1"}}), http.StatusUnprocessableEntity, "different from the temporary")
	expectRedirect(t, b.postForm("/password", url.Values{"password": {"my own password"}, "confirm": {"my own password"}}), "/")
	expect(t, b.get("/"), http.StatusOK, "Welcome to Taper", "Sam Scholar")
	expectRedirect(t, b.get("/password"), "/account")
	expectRedirect(t, e.browser().login("sam", "my own password"), "/")
}

func TestCrossOriginBlocked(t *testing.T) {
	e := newEnvReady(t)
	b := e.signedIn("admin", "admin-password")
	b.header = http.Header{"Origin": {"https://evil.example"}, "Sec-Fetch-Site": {"cross-site"}}
	r := b.post("/logout", url.Values{"csrf": {b.csrf()}})
	if r.Status != http.StatusForbidden {
		t.Fatalf("cross-site POST got %d", r.Status)
	}
	r = e.browser().post("/login", url.Values{"username": {"admin"}, "password": {"admin-password"}})
	if r.Status != http.StatusSeeOther {
		t.Fatalf("same-site login got %d", r.Status)
	}
}
