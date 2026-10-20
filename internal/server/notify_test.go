package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/taper/internal/store"
)

// noticeTitles lists a person's notifications, newest first.
func noticeTitles(t *testing.T, e *env, u *store.User) []string {
	t.Helper()
	list, err := e.store.ListNotifications(u.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(list))
	for i, n := range list {
		out[i] = n.Title
	}
	return out
}

func expectNotice(t *testing.T, e *env, u *store.User, title string) {
	t.Helper()
	for _, got := range noticeTitles(t, e, u) {
		if got == title {
			return
		}
	}
	t.Fatalf("%s wasn't told %q: %q", u.Username, title, noticeTitles(t, e, u))
}

func expectNoNotice(t *testing.T, e *env, u *store.User, part string) {
	t.Helper()
	for _, got := range noticeTitles(t, e, u) {
		if strings.Contains(got, part) {
			t.Fatalf("%s shouldn't be told %q", u.Username, got)
		}
	}
}

// at is a time today in the school's time zone.
func at(e *env, hour, minute int) time.Time {
	d := e.srv.today()
	return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, 0, 0, e.srv.loc())
}

func TestNotifications(t *testing.T) {
	c := newChatEnv(t)
	pam := c.addUser("pam", "Pam Parent", store.RoleParent, "parent-password", false)
	c.store.LinkParent(pam.ID, c.sam.ID)
	m := c.signedIn("mia", "mentor-password")
	s := c.signedIn("sam", "scholar-password")
	p := c.signedIn("pam", "parent-password")
	classPath := fmt.Sprintf("/classes/%d", c.class.ID)

	// A new assignment: the class's scholars and their parents hear about it, not the mentor or others.
	id := assignmentID(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Essay"}, "due_date": {c.tomorrow},
		"publish": {"now"}, "work_online": {"1"}}, nil))
	path := "/assignments/" + id
	expectNotice(t, c.env, c.sam, "New assignment in History of Liberty: Essay (due tomorrow)")
	expectNotice(t, c.env, pam, "New assignment in History of Liberty: Essay (due tomorrow)")
	expectNoNotice(t, c.env, c.mia, "Essay")
	expectNoNotice(t, c.env, c.zoe, "Essay")

	// A draft says nothing until it's published.
	draft := assignmentID(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Secret"}, "publish": {"draft"}}, nil))
	expectNoNotice(t, c.env, c.sam, "Secret")
	a, _ := c.store.GetAssignment(mustID(t, draft))
	a.PublishAt = time.Now().Unix() - 1
	c.store.UpdateAssignment(a)
	c.srv.announceAssignments(time.Now())
	expectNotice(t, c.env, c.sam, "New assignment in History of Liberty: Secret")
	c.srv.announceAssignments(time.Now())
	if n, _ := c.store.UnreadNotifications(c.sam.ID); n != 2 {
		t.Fatalf("announced twice? %d unread", n)
	}

	// The bell, the list, and opening one.
	expect(t, s.get("/"), http.StatusOK, `aria-label="2 unread">2</span>`)
	list := s.get("/notifications")
	expect(t, list, http.StatusOK, "2 new.", "Essay (due tomorrow)", "Mark all as read")
	first, _ := c.store.ListNotifications(c.sam.ID, 10)
	expectRedirect(t, s.get(fmt.Sprintf("/notifications/%d", first[len(first)-1].ID)), path)
	expect(t, s.get("/chat/unread"), http.StatusOK, `"notices":1`)
	expect(t, c.signedIn("zoe", "scholar-password").get(fmt.Sprintf("/notifications/%d", first[0].ID)), http.StatusNotFound)
	expectRedirect(t, s.postForm("/notifications/read", nil), "/notifications")
	expect(t, s.get("/notifications"), http.StatusOK, "All caught up", "caught up")

	// Turning work in tells the mentor; more work is combined until they look.
	c.store.AddMembers(c.class.ID, store.ClassScholar, []int64{c.zoe.ID})
	z := c.signedIn("zoe", "scholar-password")
	expectRedirect(t, s.postFiles(path+"/work", url.Values{"body": {"mine"}, "action": {"turn_in"}}, nil), path+"#work")
	expectNotice(t, c.env, c.mia, "Sam Scholar turned in Essay")
	expectRedirect(t, z.postFiles(path+"/work", url.Values{"body": {"mine too"}, "action": {"turn_in"}}, nil), path+"#work")
	expectNotice(t, c.env, c.mia, "2 pieces of work to review for Essay")
	if n, _ := c.store.UnreadNotifications(c.mia.ID); n != 1 {
		t.Fatalf("mentor's notices should be combined: %q", noticeTitles(t, c.env, c.mia))
	}

	// Feedback reaches the scholar and their parent.
	m.postForm(fmt.Sprintf("%s/work/%d", path, c.sam.ID), url.Values{"decision": {"needs_work"}, "feedback": {"Add a source."}})
	expectNotice(t, c.env, c.sam, "Essay needs another look: there's feedback")
	expectNotice(t, c.env, pam, "Sam Scholar's Essay needs another look: there's feedback")
	m.postForm(fmt.Sprintf("%s/work/%d", path, c.zoe.ID), url.Values{"decision": {"complete"}})
	expectNotice(t, c.env, c.zoe, "Essay is marked complete")

	// A parent turning work in: the mentor and the scholar hear.
	expectRedirect(t, p.postFiles(path+"/work", url.Values{"body": {"with a source"}, "action": {"turn_in"}}, nil),
		path+"?scholar="+fmt.Sprint(c.sam.ID)+"#work")
	expectNotice(t, c.env, c.sam, "Essay was turned in for you by Pam Parent")

	// Chat: everyone in the class and the parents; combined; read when the chat is opened.
	s.postForm("/notifications/read", nil)
	z.postJSON(c.cls, url.Values{"body": {"hello"}})
	expectNotice(t, c.env, c.sam, "Zoe Zed wrote in History of Liberty")
	expectNotice(t, c.env, pam, "Zoe Zed wrote in History of Liberty")
	expectNoNotice(t, c.env, c.zoe, "wrote in")
	z.postJSON(c.cls, url.Values{"body": {"anyone there?"}})
	expectNotice(t, c.env, c.sam, "2 new messages in History of Liberty")
	s.get(c.cls)
	if n, _ := c.store.UnreadNotifications(c.sam.ID); n != 0 {
		t.Fatalf("opening the chat should read its notices: %q", noticeTitles(t, c.env, c.sam))
	}

	// Turning a kind off.
	expect(t, s.get("/account"), http.StatusOK, `id="notifications"`, "Chat messages", `name="kind_chat" value="1" checked`)
	form := url.Values{"email": {"off"}}
	for _, k := range store.NotifyKinds {
		if k != store.NotifyChat {
			form.Set("kind_"+k, "1")
		}
	}
	expectRedirect(t, s.postForm("/account/notifications", form), "/account#notifications")
	z.postJSON(c.cls, url.Values{"body": {"once more"}})
	if n, _ := c.store.UnreadNotifications(c.sam.ID); n != 0 {
		t.Fatalf("chat is turned off: %q", noticeTitles(t, c.env, c.sam))
	}
	expect(t, s.postForm("/account/notifications", url.Values{"email": {"each"}}), http.StatusSeeOther)
	expect(t, s.get("/account"), http.StatusOK, "Add your email address under Profile first")

	// Calendar: a class event tells the class and parents.
	eventID(t, m.postForm("/events/new", url.Values{"calendar": {fmt.Sprint(c.class.ID)}, "title": {"Field trip"},
		"start_date": {c.tomorrow}, "all_day": {"1"}}))
	expectNoNotice(t, c.env, c.mia, "Field trip")
	expectNotice(t, c.env, pam, "New event in History of Liberty: Field trip on "+at(c.env, 12, 0).AddDate(0, 0, 1).Format("Mon, Jan 2"))
}

func TestDueReminders(t *testing.T) {
	c := newCalEnv(t)
	pam := c.addUser("pam", "Pam Parent", store.RoleParent, "parent-password", false)
	c.store.LinkParent(pam.ID, c.sam.ID)
	c.store.AddMembers(c.class.ID, store.ClassScholar, []int64{c.zoe.ID})
	m := c.signedIn("mia", "mentor-password")
	id := assignmentID(t, m.postFiles(fmt.Sprintf("/classes/%d/assignments/new", c.class.ID), url.Values{"title": {"Map"},
		"due_date": {c.tomorrow}, "due_time": {"09:00"}, "publish": {"now"}, "work_online": {"1"}}, nil))
	c.signedIn("zoe", "scholar-password").postFiles("/assignments/"+id+"/work", url.Values{"body": {"done"}, "action": {"turn_in"}}, nil)
	a, _ := c.store.GetAssignment(mustID(t, id))
	a.PublishAt = at(c.env, 8, 0).Unix() // published this morning
	c.store.UpdateAssignment(a)

	c.srv.noticeTick(at(c.env, 14, 0))
	expectNoNotice(t, c.env, c.sam, "is due")
	c.srv.noticeTick(at(c.env, 15, 30))
	expectNotice(t, c.env, c.sam, "Map is due tomorrow at 9:00 AM and isn't turned in yet")
	expectNotice(t, c.env, pam, "Sam Scholar's Map is due tomorrow at 9:00 AM and isn't turned in yet")
	expectNoNotice(t, c.env, c.zoe, "is due")
	c.srv.noticeTick(at(c.env, 16, 0))
	if n := len(noticeTitles(t, c.env, c.sam)); n != 2 { // the new assignment and one reminder
		t.Fatalf("reminded more than once: %q", noticeTitles(t, c.env, c.sam))
	}
}

func TestNotificationEmail(t *testing.T) {
	c := newCalEnv(t)
	srv := withEmail(t, c.env)
	for _, u := range []*store.User{c.sam, c.zoe} {
		u.Email = u.Username + "@home.example"
		c.store.UpdateUser(u)
	}
	c.store.AddMembers(c.class.ID, store.ClassScholar, []int64{c.zoe.ID})
	s := c.signedIn("sam", "scholar-password")
	all := url.Values{}
	for _, k := range store.NotifyKinds {
		all.Set("kind_"+k, "1")
	}
	each := url.Values{"email": {"each"}}
	daily := url.Values{"email": {"daily"}}
	for k, v := range all {
		each[k], daily[k] = v, v
	}
	expectRedirect(t, s.postForm("/account/notifications", each), "/account#notifications")
	expect(t, s.get("/account"), http.StatusOK, "an email for each notification, at sam@home.example")
	expectRedirect(t, c.signedIn("zoe", "scholar-password").postForm("/account/notifications", daily), "/account#notifications")

	// Each one: an email straight away, with a link and no content.
	m := c.signedIn("mia", "mentor-password")
	assignmentID(t, m.postFiles(fmt.Sprintf("/classes/%d/assignments/new", c.class.ID), url.Values{"title": {"Essay"},
		"instructions": {"Private instructions"}, "publish": {"now"}}, nil))
	got := waitMessages(t, c.env, srv, 1)
	if got[0].To[0] != "sam@home.example" || got[0].Subject != "New assignment in History of Liberty: Essay" ||
		strings.Contains(got[0].Body, "Private") || !strings.Contains(got[0].Body, "My account → Notifications") {
		t.Fatalf("email: %+v", got[0])
	}

	// Daily: one summary in the evening, then nothing until there's more.
	c.srv.noticeTick(at(c.env, 17, 0))
	waitMessages(t, c.env, srv, 1)
	c.srv.noticeTick(at(c.env, 18, 5))
	got = waitMessages(t, c.env, srv, 2)
	if got[1].To[0] != "zoe@home.example" || got[1].Subject != "1 notification from Liberty Commonwealth" ||
		!strings.Contains(got[1].Body, "- New assignment in History of Liberty: Essay") {
		t.Fatalf("summary: %+v", got[1])
	}
	c.srv.noticeTick(at(c.env, 18, 10))
	waitMessages(t, c.env, srv, 2)
}
