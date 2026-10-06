package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bradyloveland/taper/internal/store"
)

// fakeGitHub accepts the token "good" for the repository "school/taper".
type fakeGitHub struct {
	mu     sync.Mutex
	issues []map[string]any
	down   bool
}

func (f *fakeGitHub) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/school/taper":
			w.Write([]byte(`{"has_issues":true}`))
		case r.Method == "POST" && r.URL.Path == "/repos/school/taper/issues":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			f.issues = append(f.issues, in)
			w.WriteHeader(http.StatusCreated)
			n := len(f.issues)
			json.NewEncoder(w).Encode(map[string]any{"number": n, "html_url": "https://github.com/school/taper/issues/" + strconv.Itoa(n)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReportWithoutToken(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("sam", "Sam Stone", store.RoleScholar, "scholar-password", false)
	b := e.signedIn("sam", "scholar-password")
	expectRedirect(t, e.browser().get("/report"), "/login?next=%2Freport")
	expect(t, b.get("/"), http.StatusOK, `href="/report?from=%2f"`)
	expect(t, b.get("/report?from=/account"), http.StatusOK, "Reports are public", "<code>/account</code>")
	// Only paths on this site are kept.
	if strings.Contains(b.get("/report?from=https://evil.example").Body, "evil.example") {
		t.Fatal("an outside address was kept as the page")
	}
	expect(t, b.postForm("/report", url.Values{"title": {""}, "what": {"x"}}), http.StatusUnprocessableEntity, "short title")
	expect(t, b.postForm("/report", url.Values{"title": {"T"}, "what": {" "}}), http.StatusUnprocessableEntity, "Describe what happened")
	expect(t, b.postForm("/report", url.Values{"title": {strings.Repeat("x", 121)}, "what": {"x"}}), http.StatusUnprocessableEntity, "120 characters")

	r := b.postForm("/report", url.Values{"title": {"Save   does nothing"}, "what": {"I pressed Save, @octocat"}, "page": {"/account"}, "details": {"1"}})
	expect(t, r, http.StatusOK, "Thank you", "your school's admins can see it", "Open it on GitHub", "github.com/bradyloveland/taper/issues/new")
	list, _ := e.store.ListBugReports(10)
	if len(list) != 1 {
		t.Fatalf("reports: %d", len(list))
	}
	rep := list[0]
	if rep.Title != "Save does nothing" || rep.Page != "/account" || rep.IssueURL != "" {
		t.Fatalf("report: %+v", rep)
	}
	for _, want := range []string{"I pressed Save, @\u200boctocat", "- Page: `/account`", "- Reported by: a scholar", "- Taper version:"} {
		if !strings.Contains(rep.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, rep.Body)
		}
	}
	if strings.Contains(rep.Body, "Sam") {
		t.Fatal("the reporter's name must not go into the public report")
	}
	// Without details, none are added.
	b.postForm("/report", url.Values{"title": {"No details"}, "what": {"x"}, "page": {"/"}})
	list, _ = e.store.ListBugReports(10)
	if strings.Contains(list[0].Body, "Taper version") {
		t.Fatal("details added without asking")
	}

	// Admins see reports, with who sent them.
	a := e.signedIn("admin", "admin-password")
	expect(t, a.get("/"), http.StatusOK, "2 problem reports from your school aren't on GitHub yet")
	expect(t, a.get("/admin/reports"), http.StatusOK, "Save does nothing", "Sam Stone", "Not sent", "Open on GitHub")
	expect(t, b.get("/admin/reports"), http.StatusForbidden)
}

func TestReportThrottle(t *testing.T) {
	e := newEnvReady(t)
	b := e.signedIn("admin", "admin-password")
	for i := 0; i < 5; i++ {
		expect(t, b.postForm("/report", url.Values{"title": {"T"}, "what": {"x"}}), http.StatusOK)
	}
	expect(t, b.postForm("/report", url.Values{"title": {"T"}, "what": {"x"}}), http.StatusTooManyRequests, "wait a while")
}

func TestReportToGitHub(t *testing.T) {
	gh := &fakeGitHub{}
	ts := gh.server(t)
	e := newEnvReadyWith(t, func(o *Options) { o.GitHubAPI = ts.URL })
	a := e.signedIn("admin", "admin-password")
	expect(t, a.get("/admin/settings"), http.StatusOK, "Problem reports", "Issues: Read and write")

	// A report made before there's a token stays local.
	expect(t, a.postForm("/report", url.Values{"title": {"Early"}, "what": {"x"}}), http.StatusOK, "admins can see it")

	expect(t, a.postForm("/admin/settings/reports", url.Values{"repo": {"not a repo"}}), http.StatusUnprocessableEntity, "owner/name")
	expect(t, a.postForm("/admin/settings/reports", url.Values{"repo": {"school/taper"}, "token": {"bad"}}), http.StatusUnprocessableEntity, "didn't accept the token")
	expect(t, a.postForm("/admin/settings/reports", url.Values{"repo": {"school/other"}, "token": {"good"}}), http.StatusUnprocessableEntity, "isn't allowed to create issues")
	if e.srv.reportToken() != "" {
		t.Fatal("a token that didn't work was saved")
	}
	expectRedirect(t, a.postForm("/admin/settings/reports", url.Values{"repo": {"school/taper"}, "token": {"good"}}), "/admin/settings")
	expect(t, a.get("/admin/settings"), http.StatusOK, "A token is saved", "Remove the saved token")
	var raw string
	e.store.GetSetting(settingReportToken, &raw)
	if raw == "" || strings.Contains(raw, "good") {
		t.Fatalf("the token should be stored encrypted, got %q", raw)
	}
	if strings.Contains(a.get("/admin/settings").Body, `value="good"`) {
		t.Fatal("the token must never be shown")
	}
	// Saving again without a token keeps it.
	expectRedirect(t, a.postForm("/admin/settings/reports", url.Values{"repo": {"school/taper"}}), "/admin/settings")
	if e.srv.reportToken() != "good" {
		t.Fatal("the token should be kept")
	}

	e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	m := e.signedIn("mia", "mentor-password")
	r := m.postForm("/report", url.Values{"title": {"Calendar @here is wrong"}, "what": {"Details"}, "details": {"1"}})
	expect(t, r, http.StatusOK, "issue #1 on GitHub", "https://github.com/school/taper/issues/1")
	if len(gh.issues) != 1 || gh.issues[0]["title"] != "Calendar @\u200bhere is wrong" {
		t.Fatalf("issues: %v", gh.issues)
	}
	if labels, _ := gh.issues[0]["labels"].([]any); len(labels) != 1 || labels[0] != "bug" {
		t.Fatalf("labels: %v", gh.issues[0]["labels"])
	}
	if !strings.Contains(gh.issues[0]["body"].(string), "Reported by: a mentor") {
		t.Fatalf("body: %v", gh.issues[0]["body"])
	}

	// The earlier report can be sent now.
	page := a.get("/admin/reports")
	expect(t, page, http.StatusOK, "#1", "Early", ">Send<")
	list, _ := e.store.ListBugReports(10)
	var early *store.BugReport
	for _, rep := range list {
		if rep.Title == "Early" {
			early = rep
		}
	}
	path := "/admin/reports/" + itoa(early.ID) + "/send"
	expectRedirect(t, a.postForm(path, nil), "/admin/reports")
	expect(t, a.get("/admin/reports"), http.StatusOK, "Sent to GitHub as issue #2")
	expectRedirect(t, a.postForm(path, nil), "/admin/reports")
	expect(t, a.get("/admin/reports"), http.StatusOK, "already on GitHub")

	// If GitHub fails, the report is still saved, with the reason.
	gh.down = true
	expect(t, m.postForm("/report", url.Values{"title": {"While down"}, "what": {"x"}}), http.StatusOK, "couldn't be sent to GitHub", "Open it on GitHub")
	gh.down = false

	expectRedirect(t, a.postForm("/admin/settings/reports", url.Values{"repo": {"school/taper"}, "remove_token": {"1"}}), "/admin/settings")
	if e.srv.reportToken() != "" {
		t.Fatal("the token should be removed")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
