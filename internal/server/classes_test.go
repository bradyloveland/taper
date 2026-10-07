package server

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/store"
)

var classIDRE = regexp.MustCompile(`^/classes/(\d+)/members$`)

func TestClassLifecycle(t *testing.T) {
	e := newEnvReady(t)
	mia := e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	other := e.addUser("otto", "Otto Other", store.RoleMentor, "mentor-password", false)
	sam := e.addUser("sam", "Sam Scholar", store.RoleScholar, "scholar-password", false)
	zoe := e.addUser("zoe", "Zoe Zed", store.RoleScholar, "scholar-password", false)
	a := e.signedIn("admin", "admin-password")

	expect(t, a.get("/classes"), http.StatusOK, "No classes yet", `href="/classes/new"`)
	expect(t, a.postForm("/classes/new", url.Values{"name": {" "}}), http.StatusUnprocessableEntity, "Enter a class name")
	r := a.postForm("/classes/new", url.Values{"name": {"History  of Liberty"}, "term": {"2026–27"}, "meets": {"Tuesdays 10–11:30"},
		"color": {"teal"}, "description": {"Read **primary** sources.\n\n<script>alert(1)</script>"}})
	m := classIDRE.FindStringSubmatch(r.Location)
	if r.Status != http.StatusSeeOther || m == nil {
		t.Fatalf("create: %d %s", r.Status, r.Location)
	}
	path := "/classes/" + m[1]
	expect(t, a.get(path+"/members"), http.StatusOK, "History of Liberty is created", "Add mentors", "Mia Mentor", "Sam Scholar")

	// Add mentors and scholars; people of the wrong kind are ignored.
	expectRedirect(t, a.postForm(path+"/members/add", url.Values{"role": {"mentor"}, "user": {fmt.Sprint(mia.ID), fmt.Sprint(sam.ID)}}), path+"/members")
	expect(t, a.get(path+"/members"), http.StatusOK, "1 mentor added")
	expectRedirect(t, a.postForm(path+"/members/add", url.Values{"role": {"scholar"}, "user": {fmt.Sprint(sam.ID)}}), path+"/members")
	cl, _ := e.store.GetClass(mustID(t, m[1]))
	if cl.MentorNames() != "Mia Mentor" || cl.Scholars != 1 {
		t.Fatalf("class: %+v", cl)
	}

	page := a.get(path)
	expect(t, page, http.StatusOK, "History of Liberty", "2026–27 · Tuesdays 10–11:30", "<strong>primary</strong>", "Mia Mentor", "Sam Scholar", "Archive class")
	if strings.Contains(page.Body, "<script>alert") {
		t.Fatal("raw HTML in the description must not be rendered")
	}

	// The class's mentor can edit it and its scholars, but not its mentors.
	b := e.signedIn("mia", "mentor-password")
	expect(t, b.get("/"), http.StatusOK, "Your classes", "History of Liberty", "1 scholar · you mentor")
	expect(t, b.get(path), http.StatusOK, `href="`+path+`/edit"`)
	if strings.Contains(b.last, "Archive class") {
		t.Fatal("mentors can't archive")
	}
	expectRedirect(t, b.postForm(path+"/edit", url.Values{"name": {"History of Liberty"}, "color": {"red"}, "description": {"New"}}), path)
	expectRedirect(t, b.postForm(path+"/members/add", url.Values{"role": {"scholar"}, "user": {fmt.Sprint(zoe.ID)}}), path+"/members")
	expect(t, b.postForm(path+"/members/add", url.Values{"role": {"mentor"}, "user": {fmt.Sprint(other.ID)}}), http.StatusForbidden)
	expect(t, b.postForm(path+"/members/"+fmt.Sprint(mia.ID)+"/remove", nil), http.StatusForbidden)
	expect(t, b.postForm(path+"/archive", nil), http.StatusForbidden)
	expectRedirect(t, b.postForm(path+"/members/"+fmt.Sprint(zoe.ID)+"/remove", nil), path+"/members")

	// Another mentor can see the class but not change it.
	o := e.signedIn("otto", "mentor-password")
	expect(t, o.get("/classes"), http.StatusOK, "All classes", "History of Liberty")
	expect(t, o.get(path), http.StatusOK)
	expect(t, o.get(path+"/edit"), http.StatusForbidden)
	expect(t, o.postForm(path+"/edit", url.Values{"name": {"Hacked"}}), http.StatusForbidden)
	expect(t, o.postForm(path+"/members/add", url.Values{"role": {"scholar"}, "user": {fmt.Sprint(zoe.ID)}}), http.StatusForbidden)

	// Scholars see their own classes only, without usernames.
	s := e.signedIn("sam", "scholar-password")
	expect(t, s.get("/"), http.StatusOK, "History of Liberty", "With Mia Mentor")
	page = s.get(path)
	expect(t, page, http.StatusOK, "Mia Mentor", "Sam Scholar")
	if strings.Contains(page.Body, ">sam<") || strings.Contains(page.Body, "Members</a>") {
		t.Fatal("scholars shouldn't see usernames or management links")
	}
	expect(t, s.get("/classes"), http.StatusOK, "Your classes", "History of Liberty")
	if strings.Contains(s.last, "All classes") {
		t.Fatal("scholars don't see all classes")
	}
	z := e.signedIn("zoe", "scholar-password")
	expect(t, z.get(path), http.StatusNotFound)
	expect(t, z.get("/classes"), http.StatusOK, "not in any classes yet")
	expect(t, s.get(path+"/members"), http.StatusSeeOther)
	expect(t, s.postForm(path+"/members/add", url.Values{"role": {"scholar"}, "user": {fmt.Sprint(zoe.ID)}}), http.StatusForbidden)
	expect(t, s.get("/classes/new"), http.StatusForbidden)

	// Admins see a person's classes.
	expect(t, a.get(fmt.Sprintf("/admin/people/%d", sam.ID)), http.StatusOK, "History of Liberty")

	// Archive: hidden from current lists, read-only for mentors; delete needs the name.
	expect(t, a.postForm(path+"/delete", url.Values{"confirm": {"History of Liberty"}}), http.StatusUnprocessableEntity, "Archive it first")
	expectRedirect(t, a.postForm(path+"/archive", nil), path)
	expect(t, a.get("/classes"), http.StatusOK, "Archived classes (1)")
	expect(t, s.get("/"), http.StatusOK, "not in any classes yet")
	expect(t, b.get(path+"/edit"), http.StatusForbidden)
	expect(t, a.get("/classes?archived=1"), http.StatusOK, "History of Liberty")
	expect(t, a.postForm(path+"/delete", url.Values{"confirm": {"wrong"}}), http.StatusUnprocessableEntity, "Type the class")
	expectRedirect(t, a.postForm(path+"/unarchive", nil), path)
	expectRedirect(t, a.postForm(path+"/archive", nil), path)
	expectRedirect(t, a.postForm(path+"/delete", url.Values{"confirm": {"History of Liberty"}}), "/classes?archived=1")
	expect(t, a.get(path), http.StatusNotFound)
	expect(t, a.get("/classes/abc"), http.StatusNotFound)
	expect(t, a.postForm(path+"/nonsense", nil), http.StatusNotFound)
}

func mustID(t *testing.T, s string) int64 {
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportPeople(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("ann.adams", "Existing Ann", store.RoleScholar, "pw-123456", false)
	c := &store.Class{Name: "History of Liberty", Color: "blue"}
	e.store.CreateClass(c)
	a := e.signedIn("admin", "admin-password")
	expect(t, a.get("/admin/people"), http.StatusOK, "Import from a spreadsheet")
	expect(t, a.get("/admin/people/import"), http.StatusOK, "How to lay it out")

	expect(t, a.postForm("/admin/people/import", url.Values{"text": {""}}), http.StatusUnprocessableEntity, "Choose a spreadsheet")
	expect(t, a.postForm("/admin/people/import", url.Values{"text": {"Foo,Bar\n1,2"}}), http.StatusUnprocessableEntity, "should name the columns")

	text := "\ufeffFirst name,Last name,Email,Role,Classes\n" +
		"Ann,Adams,ann@example.org,Student,History of Liberty\n" +
		"José,Núñez,,,history of liberty\n" +
		",,,,\n" +
		"Mia,Moss,mia@example.org,teacher,History of Liberty\n" +
		"Bad,Email,not-an-email,scholar,\n" +
		"No,Class,,,Underwater Basket Weaving\n" +
		"Weird,Role,,wizard,\n"
	r := a.postForm("/admin/people/import", url.Values{"text": {text}})
	expect(t, r, http.StatusOK, "3 people are ready to add", "3 rows have problems",
		"ann.adams2", "jose.nunez", "mia.moss", "suggested",
		"email address doesn't look right", "no current class called", "role should be")
	if n, _ := e.store.CountUsers(); n != 2 {
		t.Fatal("nothing should be added before confirming")
	}

	r = a.postForm("/admin/people/import/create", url.Values{"text": {text}})
	expect(t, r, http.StatusOK, "3 people added", "shown only once", "ann.adams2", "jose.nunez")
	pw := regexp.MustCompile(`<td data-label="Temporary password"><code>([a-z]+-[a-z0-9]{4}-[a-z0-9]{4})</code>`).FindAllStringSubmatch(r.Body, -1)
	if len(pw) != 3 {
		t.Fatalf("passwords: %v", pw)
	}
	mia, err := e.store.GetUserByUsername("mia.moss")
	if err != nil || mia.Role != store.RoleMentor || mia.Email != "mia@example.org" || !mia.MustChangePassword {
		t.Fatalf("mia: %+v %v", mia, err)
	}
	cl, _ := e.store.GetClass(c.ID)
	if cl.Scholars != 2 || cl.MentorNames() != "Mia Moss" {
		t.Fatalf("class after import: %+v", cl)
	}
	jose, _ := e.store.GetUserByUsername("jose.nunez")
	if jose.DisplayName != "José Núñez" {
		t.Fatalf("name: %q", jose.DisplayName)
	}
	expectRedirect(t, e.browser().login("mia.moss", pw[2][1]), "/password")

	// The download is the list, as CSV.
	csvText := regexp.MustCompile(`(?s)<textarea name="csv" hidden>(.*?)</textarea>`).FindStringSubmatch(r.Body)
	if csvText == nil {
		t.Fatal("no CSV on the page")
	}
	form := url.Values{"csv": {strings.NewReplacer("&#34;", `"`, "&#39;", "'", "&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(csvText[1])}, "csrf": {a.csrf()}}
	req, _ := http.NewRequest("POST", e.ts.URL+"/admin/people/import/accounts.csv", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Disposition"), "taper-new-accounts.csv") ||
		!strings.Contains(string(body), "Name,Username,Temporary password") || !strings.Contains(string(body), "jose.nunez") {
		t.Fatalf("download: %q %s", res.Header.Get("Content-Disposition"), body)
	}

	// Importing the same people again finds their usernames taken.
	again := "Name,Username\nMia Moss,mia.moss\nNew Person,new.person\nNew Person 2,new.person\n"
	expect(t, a.postForm("/admin/people/import", url.Values{"text": {again}}), http.StatusOK, "Someone already has this username", "in the list twice")

	// A file upload and tab-separated text work too.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf", a.csrf())
	fw, _ := mw.CreateFormFile("file", "people.csv")
	fw.Write([]byte("Name\tRole\nTab Person\tmentor\n"))
	mw.Close()
	req, _ = http.NewRequest("POST", e.ts.URL+"/admin/people/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	expect(t, a.do(req), http.StatusOK, "1 person is ready", "tab.person", "Mentor")

	// Mentors can't import.
	e.addUser("otto", "Otto", store.RoleMentor, "mentor-password", false)
	expect(t, e.signedIn("otto", "mentor-password").postForm("/admin/people/import", url.Values{"text": {text}}), http.StatusForbidden)
}

func TestUsernameFrom(t *testing.T) {
	for in, want := range map[string]string{
		"Ann Adams": "ann.adams", "  José  Núñez ": "jose.nunez", "O'Brien, Pat": "o.brien.pat", "李": "user", "A": "user",
		"Mary-Jane Watson-Parker": "mary.jane.watson.parker",
	} {
		if got := usernameFrom(in); got != want {
			t.Errorf("usernameFrom(%q) = %q, want %q", in, got, want)
		}
	}
}
