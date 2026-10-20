package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/store"
)

func TestParents(t *testing.T) {
	c := newChatEnv(t)
	pam := c.addUser("pam", "Pam Parent", store.RoleParent, "parent-password", false)
	a := c.signedIn("admin", "admin-password")
	m := c.signedIn("mia", "mentor-password")
	classPath := fmt.Sprintf("/classes/%d", c.class.ID)

	// Before she's linked, a parent has nothing to see.
	p := c.signedIn("pam", "parent-password")
	expect(t, p.get("/"), http.StatusOK, "No scholars are linked to your account yet")
	expect(t, p.get(classPath), http.StatusNotFound)
	expect(t, p.get(c.cls), http.StatusNotFound)

	// An admin links her to Sam from her page; scholars can't be parents.
	pamPath := fmt.Sprintf("/admin/people/%d", pam.ID)
	expect(t, a.get(pamPath), http.StatusOK, "Parent of", "Not linked to any scholars", `<option value="`+fmt.Sprint(c.sam.ID)+`"`)
	expect(t, a.postForm(pamPath+"/family/add", url.Values{"person": {fmt.Sprint(c.mia.ID)}}), http.StatusSeeOther)
	expect(t, a.get(pamPath), http.StatusOK, "Only scholars have parents")
	expect(t, a.postForm(pamPath+"/family/add", url.Values{"person": {fmt.Sprint(c.sam.ID)}}), http.StatusSeeOther)
	expect(t, a.get(pamPath), http.StatusOK, "Pam Parent is now a parent of Sam Scholar", "Unlink")
	expect(t, a.get(fmt.Sprintf("/admin/people/%d", c.sam.ID)), http.StatusOK, "Parents", "Pam Parent")

	// An assignment for Sam's class.
	id := assignmentID(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Read and respond"},
		"due_date": {c.tomorrow}, "publish": {"now"}, "work_online": {"1"}, "allow_files": {"1"}}, nil))
	path := "/assignments/" + id
	draft := assignmentID(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Secret plan"}, "publish": {"draft"}}, nil))

	// Her home page shows Sam and his work; the class page shows him, not the class list.
	home := p.get("/")
	expect(t, home, http.StatusOK, "Sam Scholar", "Read and respond", "Not turned in", `href="`+path+`?scholar=`+fmt.Sprint(c.sam.ID)+`"`,
		"History of Liberty", "For Sam Scholar")
	if strings.Contains(home.Body, `id="assignments"`) || strings.Contains(home.Body, "Secret plan") {
		t.Fatal("a parent who isn't in a class has no Assignments card of their own, and no drafts")
	}
	page := p.get(classPath)
	expect(t, page, http.StatusOK, "Your scholar here", "Sam Scholar", "Read and respond", "Open class chat")
	if strings.Contains(page.Body, "Zoe") || strings.Contains(page.Body, `href="`+classPath+`/edit"`) {
		t.Fatal("parents see their own scholar, and can't edit the class")
	}
	expectRedirect(t, p.get(classPath+"/members"), classPath)
	expect(t, p.get("/assignments/"+draft), http.StatusNotFound)
	expect(t, p.get("/calendar?view=list&date="+c.today), http.StatusOK, "Due: Read and respond")

	// She writes and turns in Sam's work.
	forSam := path + "?scholar=" + fmt.Sprint(c.sam.ID)
	expect(t, p.get(path), http.StatusOK, "Sam Scholar's work", `data-autosave="`+path+`/work/autosave?scholar=`)
	expect(t, p.postForm(path+"/work/autosave", url.Values{"body": {"Sam told me his answer."}}), http.StatusOK, `"saved"`)
	expectRedirect(t, p.postFiles(path+"/work", url.Values{"body": {"Sam's answer"}, "action": {"save"}},
		map[string]string{"scan.txt": "handwritten work"}), forSam+"#work")
	workFile := fileLinkRE.FindAllStringSubmatch(p.get(forSam).Body, -1)
	expectRedirect(t, p.postFiles(path+"/work", url.Values{"body": {"Sam's answer"}, "action": {"turn_in"}}, nil), forSam+"#work")
	expect(t, p.get(forSam), http.StatusOK, "Turned in for Sam Scholar", "Take it back", "by Pam Parent (parent)")
	expect(t, p.get(workFile[len(workFile)-1][1]), http.StatusOK, "handwritten work")

	// Sam and the mentor see it as Sam's, turned in by his parent.
	expect(t, c.signedIn("sam", "scholar-password").get(path), http.StatusOK, "s answer", "by Pam Parent (parent)")
	expect(t, m.get(fmt.Sprintf("%s/work/%d", path, c.sam.ID)), http.StatusOK, "s answer", "Pam Parent (parent)")

	// She can't review, and can't touch other scholars' work.
	expect(t, p.postForm(fmt.Sprintf("%s/work/%d", path, c.sam.ID), url.Values{"decision": {"complete"}}), http.StatusNotFound)
	c.store.AddMembers(c.class.ID, store.ClassScholar, []int64{c.zoe.ID})
	expectRedirect(t, p.postForm(path+"/work/take-back?scholar="+fmt.Sprint(c.zoe.ID), nil), forSam+"#work")
	if wk, _ := c.store.GetSubmission(mustID(t, id), c.zoe.ID); wk != nil {
		t.Fatal("a parent can't start another scholar's work")
	}

	// She can read and post in the class chat, but not moderate, and only read Community.
	expect(t, p.get(c.cls), http.StatusOK, `name="body"`)
	if strings.Contains(p.last, "Manage") {
		t.Fatal("parents don't moderate")
	}
	expect(t, p.postJSON(c.cls, url.Values{"body": {"Hello from Sam's mom"}}), http.StatusOK, "Hello from Sam")
	expect(t, p.get(c.com), http.StatusOK, "Community is for news")

	// Unlinked, she loses access.
	expect(t, a.postForm(fmt.Sprintf("%s/family/%d/remove", pamPath, c.sam.ID), nil), http.StatusSeeOther)
	expect(t, p.get(classPath), http.StatusNotFound)
	expect(t, p.get(path), http.StatusNotFound)
	expect(t, p.get(workFile[len(workFile)-1][1]), http.StatusNotFound)
}

func TestParentWithTwoScholars(t *testing.T) {
	c := newCalEnv(t)
	// A mentor can be a parent too.
	c.addUser("otto", "Otto Other", store.RoleMentor, "mentor-password", false)
	otto := mustUser(t, c.env, "otto")
	c.store.AddMembers(c.class.ID, store.ClassScholar, []int64{c.zoe.ID})
	for _, k := range []int64{c.sam.ID, c.zoe.ID} {
		if _, err := c.store.LinkParent(otto.ID, k); err != nil {
			t.Fatal(err)
		}
	}
	m := c.signedIn("mia", "mentor-password")
	id := assignmentID(t, m.postFiles(fmt.Sprintf("/classes/%d/assignments/new", c.class.ID), url.Values{"title": {"Essay"},
		"due_date": {c.tomorrow}, "publish": {"now"}, "work_online": {"1"}}, nil))
	path := "/assignments/" + id
	c.signedIn("zoe", "scholar-password").postFiles(path+"/work", url.Values{"body": {"done"}, "action": {"turn_in"}}, nil)

	o := c.signedIn("otto", "mentor-password")
	expect(t, o.get(fmt.Sprintf("/classes/%d", c.class.ID)), http.StatusOK, "Your scholars here", "Sam Scholar", "Zoe Zed",
		"Sam Scholar <span class=\"badge state-todo\">", "Zoe Zed <span class=\"badge state-in\">")
	expect(t, o.get(path), http.StatusOK, `aria-label="Your scholars"`, "Sam Scholar's work")
	expect(t, o.get(path+"?scholar="+fmt.Sprint(c.zoe.ID)), http.StatusOK, "Zoe Zed's work", "Take it back")

	// Linking is only between scholars and others.
	if _, err := c.store.LinkParent(c.sam.ID, c.zoe.ID); err == nil {
		t.Fatal("a scholar can't be a parent")
	}
	// Making a parent a scholar drops their links.
	otto.Role = store.RoleScholar
	if err := c.store.UpdateUser(otto); err != nil {
		t.Fatal(err)
	}
	if kids, _ := c.store.ScholarsOf(otto.ID); len(kids) != 0 {
		t.Fatalf("links kept: %d", len(kids))
	}
}

func TestImportParents(t *testing.T) {
	c := newCalEnv(t)
	a := c.signedIn("admin", "admin-password")
	text := "Name,Role,Parent of,Classes\nAnn Adams,scholar,,History of Liberty\nPat Adams,parent,Ann Adams; sam\n" +
		"Bad Parent,parent,Nobody\nKid Parent,scholar,sam\nClassy Parent,parent,,History of Liberty\n"
	r := a.postForm("/admin/people/import", url.Values{"text": {text}})
	expect(t, r, http.StatusOK, "Ann Adams, Sam Scholar", "There&#39;s no scholar called &#34;Nobody&#34;",
		"A scholar can&#39;t be a parent", "Parents aren&#39;t put in classes")
	expect(t, a.postForm("/admin/people/import/create", url.Values{"text": {text}}), http.StatusOK, "Pat Adams", "Ann Adams, Sam Scholar")
	pat := mustUser(t, c.env, "pat.adams")
	kids, _ := c.store.ScholarsOf(pat.ID)
	if pat.Role != store.RoleParent || len(kids) != 2 {
		t.Fatalf("Pat: role %s, %d scholars", pat.Role, len(kids))
	}
}

func TestLeaderParent(t *testing.T) {
	c := newCalEnv(t)
	c.store.AddMembers(c.class.ID, store.ClassScholar, []int64{c.zoe.ID})
	bo := c.addUser("bo", "Bo Board", store.RoleBoard, "board-password", false)
	if _, err := c.store.LinkParent(bo.ID, c.sam.ID); err != nil {
		t.Fatal(err)
	}
	// A board member who's a parent still sees the whole class, and their scholar.
	expect(t, c.signedIn("bo", "board-password").get(fmt.Sprintf("/classes/%d", c.class.ID)), http.StatusOK,
		"Your scholar here", "Scholars <span class=\"muted\">(2)</span>", "Zoe Zed", "Archive class")
}
