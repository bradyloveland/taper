package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/store"
)

var tempPasswordRE = regexp.MustCompile(`id="temp-password" class="big">([^<]+)<`)

func tempPassword(t *testing.T, body string) string {
	t.Helper()
	m := tempPasswordRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no temporary password on the page:\n%s", body)
	}
	return m[1]
}

func TestRoleAccess(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	e.addUser("sam", "Sam Scholar", store.RoleScholar, "scholar-password", false)
	for _, who := range []string{"mia", "sam"} {
		pw := map[string]string{"mia": "mentor-password", "sam": "scholar-password"}[who]
		b := e.signedIn(who, pw)
		home := b.get("/")
		expect(t, home, http.StatusOK)
		if regexp.MustCompile(`href="/admin/`).MatchString(home.Body) {
			t.Fatalf("%s sees admin links", who)
		}
		for _, p := range []string{"/admin/people", "/admin/people/new", "/admin/people/1", "/admin/settings"} {
			expect(t, b.get(p), http.StatusForbidden, "Admins only")
		}
		expect(t, b.postForm("/admin/people/new", url.Values{"display_name": {"X"}, "username": {"xx"}, "role": {"admin"}}), http.StatusForbidden)
		expect(t, b.postForm("/admin/settings", url.Values{"school": {"Hacked"}}), http.StatusForbidden)
	}
	if e.store.SchoolName() != "Liberty Commonwealth" {
		t.Fatal("non-admin changed settings")
	}
	if _, err := e.store.GetUserByUsername("xx"); err == nil {
		t.Fatal("non-admin added a person")
	}
}

func TestAddPerson(t *testing.T) {
	e := newEnvReady(t)
	b := e.signedIn("admin", "admin-password")
	expect(t, b.get("/admin/people/new?role=mentor"), http.StatusOK, `<option value="mentor" selected>`)

	form := url.Values{"display_name": {"  Mia   Moss "}, "username": {"Mia.Moss"}, "email": {"mia@example.org"}, "role": {"mentor"}}
	r := b.postForm("/admin/people/new", form)
	expect(t, r, http.StatusCreated, "Mia Moss is added", "mia.moss", "Temporary password")
	temp := tempPassword(t, r.Body)
	u, err := e.store.GetUserByUsername("mia.moss")
	if err != nil || u.DisplayName != "Mia Moss" || u.Role != store.RoleMentor || !u.MustChangePassword {
		t.Fatalf("person saved wrong: %+v %v", u, err)
	}

	// Duplicate usernames and bad input are refused.
	expect(t, b.postForm("/admin/people/new", form), http.StatusUnprocessableEntity, "already has the username")
	form.Set("username", "other")
	form.Set("email", "not an email")
	expect(t, b.postForm("/admin/people/new", form), http.StatusUnprocessableEntity, "email address")
	form.Set("email", "")
	form.Set("role", "wizard")
	expect(t, b.postForm("/admin/people/new", form), http.StatusUnprocessableEntity, "Choose a role")

	// The list shows them, with the temporary-password badge.
	expect(t, b.get("/admin/people"), http.StatusOK, "Mia Moss", "Temporary password")
	expect(t, b.get("/admin/people?role=scholar"), http.StatusOK, "No one matches")
	expect(t, b.get("/admin/people?q=example.org"), http.StatusOK, "Mia Moss")

	// They can sign in with the temporary password and must choose their own.
	m := e.browser()
	expectRedirect(t, m.login("mia.moss", temp), "/password")
}

func TestEditPerson(t *testing.T) {
	e := newEnvReady(t)
	sam := e.addUser("sam", "Sam Scholar", store.RoleScholar, "scholar-password", false)
	samBrowser := e.signedIn("sam", "scholar-password")
	b := e.signedIn("admin", "admin-password")
	path := fmt.Sprintf("/admin/people/%d", sam.ID)

	expect(t, b.get(path), http.StatusOK, "Sam Scholar", "Can sign in")
	expect(t, b.get("/admin/people/9999"), http.StatusNotFound)
	expect(t, b.get("/admin/people/abc"), http.StatusNotFound)

	form := url.Values{"display_name": {"Samuel Stone"}, "username": {"sam"}, "email": {""}, "role": {"mentor"}, "active": {"1"}}
	expectRedirect(t, b.postForm(path, form), path)
	expect(t, b.get(path), http.StatusOK, "Saved.")
	got, _ := e.store.GetUser(sam.ID)
	if got.DisplayName != "Samuel Stone" || got.Role != store.RoleMentor {
		t.Fatalf("not saved: %+v", got)
	}

	// Deactivating signs them out everywhere.
	form.Del("active")
	expectRedirect(t, b.postForm(path, form), path)
	expect(t, b.get(path), http.StatusOK, "can&#39;t sign in anymore")
	expectRedirect(t, samBrowser.get("/"), "/login")
	expect(t, b.get("/admin/people"), http.StatusOK)
	if r := b.get("/admin/people"); regexp.MustCompile(`Samuel Stone`).MatchString(r.Body) {
		t.Fatal("deactivated people should be hidden by default")
	}
	expect(t, b.get("/admin/people?status=inactive"), http.StatusOK, "Samuel Stone", "Deactivated")

	form.Set("active", "1")
	expectRedirect(t, b.postForm(path, form), path)
	expect(t, b.get(path), http.StatusOK, "can sign in again")
}

func TestAdminGuards(t *testing.T) {
	e := newEnvReady(t)
	admin, _ := e.store.GetUserByUsername("admin")
	b := e.signedIn("admin", "admin-password")
	self := fmt.Sprintf("/admin/people/%d", admin.ID)

	// Admins can't demote or deactivate themselves; those fields are ignored.
	expect(t, b.get(self), http.StatusOK, "can't change your own role")
	r := b.postForm(self, url.Values{"display_name": {"Ada A."}, "username": {"admin"}, "role": {"scholar"}})
	expectRedirect(t, r, self)
	got, _ := e.store.GetUser(admin.ID)
	if !got.IsAdmin() || !got.Active || got.DisplayName != "Ada A." {
		t.Fatalf("self-edit changed role or status: %+v", got)
	}
	expect(t, b.postForm(self+"/reset-password", nil), http.StatusUnprocessableEntity, "My account")

	// With two admins, one can demote the other, who then loses admin pages.
	bea := e.addUser("bea", "Bea Boss", store.RoleAdmin, "bea-password", false)
	beaBrowser := e.signedIn("bea", "bea-password")
	beaPath := fmt.Sprintf("/admin/people/%d", bea.ID)
	expectRedirect(t, b.postForm(beaPath, url.Values{"display_name": {"Bea"}, "username": {"bea"}, "role": {"mentor"}, "active": {"1"}}), beaPath)
	expect(t, beaBrowser.get("/admin/people"), http.StatusForbidden)
	if n, _ := e.store.ActiveAdmins(); n != 1 {
		t.Fatalf("active admins = %d", n)
	}
}

// The last-admin rule is a safety net: in normal use the admin making the
// change is an active admin too. Check it on the handler directly by
// editing the only admin from a request whose signed-in user is a stale copy.
func TestLastAdminRefused(t *testing.T) {
	e := newEnvReady(t)
	admin, _ := e.store.GetUserByUsername("admin")
	ghost := &store.User{ID: 999_999, Role: store.RoleAdmin, Active: true, Username: "ghost"}
	sess := &store.Session{CSRF: "x"}
	form := url.Values{"display_name": {"Ada"}, "username": {"admin"}, "role": {"mentor"}, "active": {"1"}}
	req := httptest.NewRequest("POST", fmt.Sprintf("/admin/people/%d", admin.ID), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", fmt.Sprint(admin.ID))
	req = req.WithContext(context.WithValue(req.Context(), ctxKey{}, &reqInfo{user: ghost, sess: sess}))
	w := httptest.NewRecorder()
	e.srv.handlePersonUpdate(w, req)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "only admin") {
		t.Fatalf("status %d\n%s", w.Code, w.Body.String())
	}
	if got, _ := e.store.GetUser(admin.ID); !got.IsAdmin() {
		t.Fatal("the only admin was demoted")
	}
}

func TestResetPassword(t *testing.T) {
	e := newEnvReady(t)
	sam := e.addUser("sam", "Sam Scholar", store.RoleScholar, "scholar-password", false)
	samBrowser := e.signedIn("sam", "scholar-password")
	b := e.signedIn("admin", "admin-password")
	r := b.postForm(fmt.Sprintf("/admin/people/%d/reset-password", sam.ID), nil)
	expect(t, r, http.StatusOK, "New temporary password for Sam Scholar")
	temp := tempPassword(t, r.Body)
	expectRedirect(t, samBrowser.get("/"), "/login")
	expect(t, e.browser().login("sam", "scholar-password"), http.StatusUnauthorized)
	expectRedirect(t, e.browser().login("sam", temp), "/password")
}
