package server

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/store"
)

var (
	secretRE   = regexp.MustCompile(`name="secret" value="([A-Z2-7]{32})"`)
	recoveryRE = regexp.MustCompile(`<code>([a-z2-9]{5}-[a-z2-9]{5})</code>`)
)

// totpNow returns the code for the current step plus offset. It avoids the
// last seconds of a step, so the step can't change before the server checks
// the code (which made this test flaky under the race detector).
func totpNow(t *testing.T, secret string, offset int64) string {
	t.Helper()
	if into := time.Now().Unix() % auth.TOTPPeriod; into >= auth.TOTPPeriod-3 {
		time.Sleep(time.Duration(auth.TOTPPeriod-into+1) * time.Second)
	}
	c, err := auth.TOTPCode(secret, time.Now().Unix()/auth.TOTPPeriod+offset, 6)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// enableTwoStep turns on two-step sign-in through the pages and returns the
// secret and recovery codes.
func enableTwoStep(t *testing.T, b *browser) (string, []string) {
	t.Helper()
	page := b.get("/account/two-step")
	expect(t, page, http.StatusOK, "Set it up", "<svg", "QR code")
	m := secretRE.FindStringSubmatch(page.Body)
	if m == nil {
		t.Fatalf("no secret on the page:\n%s", page.Body)
	}
	secret := m[1]
	expect(t, b.postForm("/account/two-step/enable", url.Values{"secret": {secret}, "code": {"000000"}}), http.StatusUnprocessableEntity, "isn't right")
	r := b.postForm("/account/two-step/enable", url.Values{"secret": {secret}, "code": {totpNow(t, secret, -1)}})
	expect(t, r, http.StatusOK, "Your recovery codes", "won't be shown again")
	var codes []string
	for _, m := range recoveryRE.FindAllStringSubmatch(r.Body, -1) {
		codes = append(codes, m[1])
	}
	if len(codes) != 10 {
		t.Fatalf("recovery codes: %v", codes)
	}
	return secret, codes
}

func TestTwoStepSignIn(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	other := e.signedIn("mia", "mentor-password")
	b := e.signedIn("mia", "mentor-password")
	expect(t, b.get("/account"), http.StatusOK, "Set up two-step sign-in")
	secret, codes := enableTwoStep(t, b)
	expectRedirect(t, other.get("/"), "/login") // other browsers are signed out
	u, _ := e.store.GetUserByUsername("mia")
	if !u.TOTPEnabled || u.TOTPSecret == "" || strings.Contains(u.TOTPSecret, secret) {
		t.Fatal("the secret should be stored, encrypted")
	}
	expect(t, b.get("/account/two-step"), http.StatusOK, "Two-step sign-in is on", "<strong>10</strong> unused recovery codes")

	// Signing in now needs a code.
	c := e.browser()
	r := c.post("/login", url.Values{"username": {"mia"}, "password": {"mentor-password"}, "next": {"/account"}, "remember": {"1"}})
	expectRedirect(t, r, "/login/verify?next=%2Faccount")
	if strings.Contains(r.Header.Get("Set-Cookie"), "Max-Age") {
		t.Fatal("a half-signed-in session shouldn't get a lasting cookie")
	}
	// Nothing else is open until the code is entered.
	expectRedirect(t, c.get("/"), "/login/verify")
	expectRedirect(t, c.get("/account"), "/login/verify")
	page := c.get("/login/verify?next=/account")
	expect(t, page, http.StatusOK, "Two-step sign-in", "authenticator app")
	if strings.Contains(page.Body, `href="/account"`) {
		t.Fatal("the menu shouldn't show before the second step")
	}
	expect(t, c.postForm("/login/verify", url.Values{"code": {"123456"}, "next": {"/account"}}), http.StatusUnauthorized, "isn't right")
	r = c.postForm("/login/verify", url.Values{"code": {totpNow(t, secret, 0)}, "next": {"/account"}})
	expectRedirect(t, r, "/account")
	if !strings.Contains(r.Header.Get("Set-Cookie"), "Max-Age=2592000") {
		t.Fatalf("remembered session cookie after the second step: %s", r.Header.Get("Set-Cookie"))
	}
	expect(t, c.get("/account"), http.StatusOK, "My account")

	// The same code can't be used twice.
	d := e.browser()
	d.login("mia", "mentor-password")
	d.get("/login/verify")
	expect(t, d.postForm("/login/verify", url.Values{"code": {totpNow(t, secret, 0)}}), http.StatusUnauthorized)

	// A recovery code works once.
	expectRedirect(t, d.postForm("/login/verify", url.Values{"code": {strings.ToUpper(codes[0])}}), "/")
	expect(t, d.get("/"), http.StatusOK, "You used a recovery code. You have 9 left.")
	f := e.browser()
	f.login("mia", "mentor-password")
	f.get("/login/verify")
	expect(t, f.postForm("/login/verify", url.Values{"code": {codes[0]}}), http.StatusUnauthorized, "already used")
}

func TestTwoStepThrottle(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	enableTwoStep(t, e.signedIn("mia", "mentor-password"))
	c := e.browser()
	c.login("mia", "mentor-password")
	c.get("/login/verify")
	for i := 0; i < 5; i++ {
		expect(t, c.postForm("/login/verify", url.Values{"code": {"000000"}}), http.StatusUnauthorized)
	}
	expectRedirect(t, c.postForm("/login/verify", url.Values{"code": {"000000"}}), "/login")
	expect(t, c.get("/login"), http.StatusOK, "Too many wrong codes")
}

func TestTwoStepManage(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	b := e.signedIn("mia", "mentor-password")
	_, codes := enableTwoStep(t, b)
	expect(t, b.postForm("/account/two-step/recovery", url.Values{"password": {"wrong"}}), http.StatusUnprocessableEntity, "password isn't right")
	r := b.postForm("/account/two-step/recovery", url.Values{"password": {"mentor-password"}})
	expect(t, r, http.StatusOK, "Your recovery codes")
	if strings.Contains(r.Body, codes[0]) {
		t.Fatal("new codes should differ")
	}
	u, _ := e.store.GetUserByUsername("mia")
	if ok, _ := e.store.UseRecoveryCode(u.ID, auth.HashRecovery(codes[0])); ok {
		t.Fatal("old codes should stop working")
	}
	expect(t, b.postForm("/account/two-step/disable", url.Values{"password": {"wrong"}}), http.StatusUnprocessableEntity)
	expectRedirect(t, b.postForm("/account/two-step/disable", url.Values{"password": {"mentor-password"}}), "/account/two-step")
	expectRedirect(t, e.browser().login("mia", "mentor-password"), "/")
}

func TestTwoStepPolicyAndAdminReset(t *testing.T) {
	e := newEnvReady(t)
	mia := e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	e.addUser("sam", "Sam Scholar", store.RoleScholar, "scholar-password", false)
	a := e.signedIn("admin", "admin-password")
	expect(t, a.get("/admin/settings"), http.StatusOK, "Require it for mentors")
	expectRedirect(t, a.postForm("/admin/settings/security", url.Values{"mentor": {"1"}}), "/admin/settings#security")

	// Mentors must set it up; scholars and admins needn't.
	m := e.signedIn("mia", "mentor-password")
	expectRedirect(t, m.get("/"), "/account/two-step")
	expectRedirect(t, m.get("/guide"), "/account/two-step")
	expect(t, m.get("/account/two-step"), http.StatusOK, "Your school requires two-step sign-in for mentors")
	expect(t, e.signedIn("sam", "scholar-password").get("/"), http.StatusOK)
	expect(t, a.get("/"), http.StatusOK)
	_, _ = enableTwoStep(t, m)
	expect(t, m.get("/"), http.StatusOK)
	expect(t, m.get("/account/two-step"), http.StatusOK, "so it stays on")
	expect(t, m.postForm("/account/two-step/disable", url.Values{"password": {"mentor-password"}}), http.StatusUnprocessableEntity, "requires two-step")

	// An admin turns it off for someone who lost their phone.
	path := fmt.Sprintf("/admin/people/%d", mia.ID)
	expect(t, a.get(path), http.StatusOK, "Turn off two-step sign-in")
	expectRedirect(t, a.postForm(path+"/two-step-off", nil), path)
	expect(t, a.get(path), http.StatusOK, "set it up again")
	expectRedirect(t, m.get("/"), "/login") // signed out
	admin, _ := e.store.GetUserByUsername("admin")
	expect(t, a.postForm(fmt.Sprintf("/admin/people/%d/two-step-off", admin.ID), nil), http.StatusUnprocessableEntity, "My account")
	// Requiring it for admins applies to the admin who saved it.
	expectRedirect(t, a.postForm("/admin/settings/security", url.Values{"admin": {"1"}}), "/admin/settings#security")
	expectRedirect(t, a.get("/"), "/account/two-step")
}
