package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/store"
)

func TestBoardRole(t *testing.T) {
	c := newChatEnv(t)
	c.addUser("bo", "Bo Board", store.RoleBoard, "board-password", false)
	b := c.signedIn("bo", "board-password")
	admin, _ := c.store.GetUserByUsername("admin")

	// The menu has People, not Settings; server pages are for admins only.
	home := b.get("/")
	expect(t, home, http.StatusOK, `href="/admin/people"`, "Classes</span>")
	if strings.Contains(home.Body, `href="/admin/settings"`) {
		t.Fatal("board members don't get Settings")
	}
	for _, p := range []string{"/admin/settings", "/admin/updates", "/admin/network", "/admin/email", "/admin/reports", "/admin/backup"} {
		expect(t, b.get(p), http.StatusForbidden, "Admins only")
	}
	expect(t, b.postForm("/admin/settings", url.Values{"school": {"Board Academy"}}), http.StatusForbidden)

	// Classes, calendars and assignments: like an admin.
	expect(t, b.get("/classes/new"), http.StatusOK)
	expect(t, b.get(classPath(c.class.ID)), http.StatusOK, "Archive class", "Members")
	expect(t, b.get(classPath(c.class.ID)+"/members"), http.StatusOK, "Add mentors")
	expect(t, b.get("/events/new?cal=school"), http.StatusOK, "School calendar")
	eventID(t, b.postForm("/events/new", url.Values{"calendar": {"school"}, "title": {"Board meeting"}, "start_date": {c.today},
		"start_time": {"18:00"}, "end_time": {"19:00"}}))
	m := c.signedIn("mia", "mentor-password")
	id := assignmentID(t, m.postFiles(classPath(c.class.ID)+"/assignments/new", url.Values{"title": {"Essay"}, "publish": {"draft"}}, nil))
	expect(t, b.get("/assignments/"+id), http.StatusOK, "Scholars' work")

	// People: mentors and scholars, but not admin or board accounts.
	expect(t, b.get("/admin/people"), http.StatusOK, "Admin", "Mia Mentor")
	form := b.get("/admin/people/new?role=admin")
	expect(t, form, http.StatusOK, `value="scholar" selected`)
	if strings.Contains(form.Body, `<option value="admin"`) || strings.Contains(form.Body, `<option value="board"`) {
		t.Fatal("board members can't make admins or board members")
	}
	expect(t, b.postForm("/admin/people/new", url.Values{"display_name": {"Eve"}, "username": {"eve"}, "role": {"admin"}}), http.StatusUnprocessableEntity, "Choose a role")
	expect(t, b.postForm("/admin/people/new", url.Values{"display_name": {"Max Mentor"}, "username": {"max"}, "role": {"mentor"}}), http.StatusCreated)
	adminPath := fmt.Sprintf("/admin/people/%d", admin.ID)
	expect(t, b.get(adminPath), http.StatusForbidden, "Only admins can change this account")
	expect(t, b.postForm(adminPath+"/reset-password", nil), http.StatusForbidden)
	expect(t, b.postForm(adminPath+"/two-step-off", nil), http.StatusForbidden)
	expect(t, b.postForm(adminPath, url.Values{"display_name": {"Hacked"}, "username": {"admin"}, "role": {"scholar"}}), http.StatusForbidden)
	samPath := fmt.Sprintf("/admin/people/%d", c.sam.ID)
	expect(t, b.get(samPath), http.StatusOK)
	expect(t, b.postForm(samPath, url.Values{"display_name": {"Sam Scholar"}, "username": {"sam"}, "role": {"board"}, "active": {"1"}}),
		http.StatusUnprocessableEntity, "Choose a role")
	expect(t, b.postForm(fmt.Sprintf("/admin/people/%d/reset-password", mustUser(t, c.env, "max").ID), nil), http.StatusOK, "New temporary password")
	expect(t, b.postForm("/admin/people/import", url.Values{"text": {"Name,Role\nNew Admin,admin\nNew Kid,scholar"}}), http.StatusOK,
		"Only admins can add admins and board members")

	// Admins can make board members.
	a := c.signedIn("admin", "admin-password")
	expect(t, a.get("/admin/people/new"), http.StatusOK, `<option value="board"`)
	expect(t, a.postForm("/admin/people/new", url.Values{"display_name": {"Bea"}, "username": {"bea"}, "role": {"board"}}), http.StatusCreated, "Add another board member")

	// Chat: every chat, posting in Community, moderating.
	expect(t, b.get("/chat"), http.StatusOK, "Other chats", "History of Liberty")
	expect(t, b.get(c.cls), http.StatusOK, "Manage", `name="body"`)
	expect(t, b.postJSON(c.com, url.Values{"body": {"From the board"}}), http.StatusOK, "From the board")
	expect(t, c.signedIn("zoe", "scholar-password").get(c.com), http.StatusOK, "Bo Board</strong> <span class=\"badge role-board\">Board</span>")
	s := c.signedIn("sam", "scholar-password")
	s.postForm(c.cls, url.Values{"body": {"hi"}})
	msg := lastMessage(t, c.env, c.cls)
	expect(t, b.postJSON(fmt.Sprintf("%s/messages/%d/delete", c.cls, msg.ID), nil), http.StatusOK, "removed by a moderator")
	expectRedirect(t, b.postForm(c.cls+"/mute", url.Values{"user": {fmt.Sprint(c.sam.ID)}, "for": {"hour"}}), c.cls)
	// Board members lead every chat, so they can't be muted.
	expectRedirect(t, a.postForm(c.com+"/mute", url.Values{"user": {fmt.Sprint(mustUser(t, c.env, "bo").ID)}, "for": {"hour"}}), c.com)
	expect(t, a.get(c.com), http.StatusOK, "can't be muted here")
}

func mustUser(t *testing.T, e *env, username string) *store.User {
	t.Helper()
	u, err := e.store.GetUserByUsername(username)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

var chatIDRE = regexp.MustCompile(`^/chat/(\d+)$`)

func TestGroupChats(t *testing.T) {
	c := newChatEnv(t)
	c.addUser("bo", "Bo Board", store.RoleBoard, "board-password", false)
	a := c.signedIn("admin", "admin-password")
	s := c.signedIn("sam", "scholar-password")
	z := c.signedIn("zoe", "scholar-password")
	m := c.signedIn("mia", "mentor-password")
	b := c.signedIn("bo", "board-password")

	expect(t, s.get("/chat/new"), http.StatusForbidden)
	expect(t, m.get("/chat/new"), http.StatusForbidden)
	expect(t, a.get("/chat"), http.StatusOK, `href="/chat/new"`)
	expect(t, a.get("/chat/new"), http.StatusOK, "Sam Scholar", "Zoe Zed")
	expect(t, a.postForm("/chat/new", url.Values{"name": {"Garden club"}}), http.StatusUnprocessableEntity, "Choose at least one person")
	r := a.postForm("/chat/new", url.Values{"name": {"Garden club"}, "user": {fmt.Sprint(c.sam.ID), fmt.Sprint(c.zoe.ID)}})
	mm := chatIDRE.FindStringSubmatch(r.Location)
	if r.Status != http.StatusSeeOther || mm == nil {
		t.Fatalf("create group: %d %s", r.Status, r.Location)
	}
	g := r.Location
	expect(t, a.get(g), http.StatusOK, "Garden club is ready, with 3 people", "Group chat · 3 people", `href="`+g+`/people"`)

	// Members see it under Groups; others can't.
	expect(t, s.get("/chat"), http.StatusOK, "Groups", "Garden club")
	expect(t, z.get(g), http.StatusOK, `name="body"`)
	expect(t, m.get(g), http.StatusNotFound)
	if strings.Contains(m.get("/chat").Body, "Garden club") {
		t.Fatal("non-members don't see the group")
	}
	expect(t, b.get("/chat"), http.StatusOK, "Other chats", "Garden club") // the board sees every chat
	expect(t, b.get(g), http.StatusOK, "Manage")
	expect(t, s.postJSON(g, url.Values{"body": {"Seeds arrive Friday"}}), http.StatusOK)
	expect(t, z.get("/chat/unread"), http.StatusOK, `"total":1`)

	// Only leaders look after it.
	expect(t, s.get(g+"/people"), http.StatusForbidden)
	expect(t, s.postForm(g+"/people/add", url.Values{"user": {fmt.Sprint(c.mia.ID)}}), http.StatusForbidden)
	expect(t, b.get(g+"/people"), http.StatusOK, "Sam Scholar", "Mia Mentor")
	expectRedirect(t, b.postForm(g+"/people/add", url.Values{"user": {fmt.Sprint(c.mia.ID)}}), g+"/people")
	expect(t, m.get(g), http.StatusOK, "Seeds arrive Friday")
	expectRedirect(t, a.postForm(fmt.Sprintf("%s/people/%d/remove", g, c.zoe.ID), nil), g+"/people")
	expect(t, z.get(g), http.StatusNotFound)
	expectRedirect(t, a.postForm(g+"/rename", url.Values{"name": {"Garden committee"}}), g+"/people")
	expect(t, s.get(g), http.StatusOK, "Garden committee")
	expect(t, a.postForm(g+"/delete", url.Values{"confirm": {"Garden club"}}), http.StatusUnprocessableEntity, "Type the chat's name exactly")
	expectRedirect(t, a.postForm(g+"/delete", url.Values{"confirm": {"Garden committee"}}), "/chat")
	expect(t, s.get(g), http.StatusNotFound)

	// Class and community chats can't be managed like groups.
	expect(t, a.get(c.cls+"/people"), http.StatusNotFound)
	expect(t, a.postForm(c.com+"/delete", url.Values{"confirm": {"Community"}}), http.StatusNotFound)
}

// postChatFiles posts a message with files the way the page's script does.
func (b *browser) postChatFiles(path, body string, files map[string][]byte) *resp {
	b.e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf", b.csrf())
	mw.WriteField("body", body)
	for name, content := range files {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write(content)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", b.url()+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	return b.do(req)
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

func TestChatAttachments(t *testing.T) {
	c := newChatEnv(t)
	s := c.signedIn("sam", "scholar-password")
	m := c.signedIn("mia", "mentor-password")
	z := c.signedIn("zoe", "scholar-password")

	r := s.postChatFiles(c.cls, "", map[string][]byte{"photo.png": pngBytes, "notes.txt": []byte("my notes")})
	var j struct{ HTML string }
	if err := json.Unmarshal([]byte(r.Body), &j); err != nil || r.Status != http.StatusOK {
		t.Fatalf("post with files: %d %s", r.Status, r.Body)
	}
	img := regexp.MustCompile(`<img src="(/files/\d+/photo.png)"`).FindStringSubmatch(j.HTML)
	doc := regexp.MustCompile(`href="(/files/\d+/notes.txt)\?download=1"`).FindStringSubmatch(j.HTML)
	if img == nil || doc == nil {
		t.Fatalf("message HTML lacks the files:\n%s", j.HTML)
	}
	expect(t, m.get(img[1]), http.StatusOK)
	expect(t, m.get(doc[1]), http.StatusOK, "my notes")
	expect(t, z.get(img[1]), http.StatusNotFound) // not in the class
	expect(t, m.get(c.cls), http.StatusOK, `<img src="`+img[1]+`"`)
	expect(t, c.signedIn("admin", "admin-password").get("/chat"), http.StatusOK)

	many := map[string][]byte{}
	for i := 0; i <= maxChatFiles; i++ {
		many[fmt.Sprintf("f%d.txt", i)] = []byte("x")
	}
	expect(t, s.postChatFiles(c.cls, "lots", many), http.StatusUnprocessableEntity, "Send up to")

	// Removing the message removes its files.
	msg := lastMessage(t, c.env, c.cls)
	if n := countFiles(t, c.srv.filesDir()); n != 2 {
		t.Fatalf("%d files stored", n)
	}
	expect(t, s.postJSON(fmt.Sprintf("%s/messages/%d/delete", c.cls, msg.ID), nil), http.StatusOK)
	if n := countFiles(t, c.srv.filesDir()); n != 0 {
		t.Fatalf("%d files left after removing the message", n)
	}
	expect(t, m.get(img[1]), http.StatusNotFound)
}
