package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/taper/internal/store"
)

// chatEnv is a calEnv (a class with Mia mentoring and Sam in it, and Zoe
// outside it) with the community and class channels.
type chatEnv struct {
	*calEnv
	com, cls string // their paths
}

func newChatEnv(t *testing.T) *chatEnv {
	c := newCalEnv(t)
	com, _ := c.store.CommunityChannel()
	cls, _ := c.store.ClassChannel(c.class.ID)
	return &chatEnv{calEnv: c, com: chatPath(com.ID), cls: chatPath(cls.ID)}
}

// postJSON posts a form the way the page's script does.
func (b *browser) postJSON(path string, form url.Values) *resp {
	b.e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", b.csrf())
	req, _ := http.NewRequest("POST", b.url()+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return b.do(req)
}

func lastMessage(t *testing.T, e *env, channelPath string) *store.Message {
	t.Helper()
	id := mustID(t, strings.TrimPrefix(channelPath, "/chat/"))
	list, _, err := e.store.ListMessages(id, 0, 1)
	if err != nil || len(list) == 0 {
		t.Fatalf("no messages in %s: %v", channelPath, err)
	}
	return list[0]
}

func TestChatChannels(t *testing.T) {
	c := newChatEnv(t)
	c.addUser("otto", "Otto Other", store.RoleMentor, "mentor-password", false)
	s := c.signedIn("sam", "scholar-password")
	z := c.signedIn("zoe", "scholar-password")
	m := c.signedIn("mia", "mentor-password")
	o := c.signedIn("otto", "mentor-password")
	a := c.signedIn("admin", "admin-password")

	expect(t, s.get("/chat"), http.StatusOK, "Community", "History of Liberty", `href="`+c.cls+`"`)
	expect(t, z.get("/chat"), http.StatusOK, "Community")
	if strings.Contains(z.last, "History of Liberty") {
		t.Fatal("class chat is for its members")
	}
	expect(t, z.get(c.cls), http.StatusNotFound)
	expect(t, o.get(c.cls), http.StatusNotFound) // mentors outside the class too
	expect(t, a.get(c.cls), http.StatusOK, "History of Liberty", "Manage")
	expect(t, s.get(c.cls), http.StatusOK, "No messages yet", `placeholder="Message History of Liberty"`)
	if strings.Contains(s.last, "Manage") {
		t.Fatal("scholars don't manage chat")
	}

	// Posting without script: redirect to the message. Text is escaped and
	// links work.
	r := s.postForm(c.com, url.Values{"body": {"Hi all <script>alert(1)</script>\nsee https://example.org/a?b=1."}})
	msg := lastMessage(t, c.env, c.com)
	expectRedirect(t, r, fmt.Sprintf("%s#m%d", c.com, msg.ID))
	page := z.get(c.com)
	expect(t, page, http.StatusOK, "Sam Scholar", "Hi all &lt;script&gt;", `<a href="https://example.org/a?b=1" rel="noopener noreferrer nofollow" target="_blank">`, "Today")
	if strings.Contains(page.Body, "<script>alert") {
		t.Fatal("message HTML must be escaped")
	}
	expect(t, s.postForm(c.com, url.Values{"body": {"   "}}), http.StatusUnprocessableEntity, "Write a message first")
	expect(t, s.postForm(c.com, url.Values{"body": {strings.Repeat("x", maxChatMessage+1)}}), http.StatusUnprocessableEntity, "too long")
	expect(t, z.postForm(c.cls, url.Values{"body": {"sneaking in"}}), http.StatusNotFound)

	// With script: JSON with the rendered message.
	r = m.postJSON(c.cls, url.Values{"body": {"Welcome to class"}})
	var j struct {
		ID   int64
		HTML string
	}
	if err := json.Unmarshal([]byte(r.Body), &j); err != nil || r.Status != http.StatusOK || !strings.Contains(j.HTML, "Welcome to class") ||
		!strings.Contains(j.HTML, "Mentor</span>") {
		t.Fatalf("JSON post: %d %s", r.Status, r.Body)
	}

	// Unread counts in the menu, and on the class page.
	expect(t, s.get("/chat/unread"), http.StatusOK, `"total":1`)
	expect(t, s.get("/"), http.StatusOK, `aria-label="1 unread">1</span>`)
	expect(t, s.get(classPath(c.class.ID)), http.StatusOK, "Class chat", "1 new message")
	s.get(c.cls) // reading it clears it
	expect(t, s.get("/chat/unread"), http.StatusOK, `"total":0`)
	expect(t, m.get("/chat/unread"), http.StatusOK, `"total":1`) // Sam's community message
	r = m.postJSON(c.com+"/read", url.Values{"last": {fmt.Sprint(msg.ID)}})
	expect(t, r, http.StatusOK, `"total":0`)
}

func TestChatModeration(t *testing.T) {
	c := newChatEnv(t)
	s := c.signedIn("sam", "scholar-password")
	m := c.signedIn("mia", "mentor-password")
	a := c.signedIn("admin", "admin-password")
	z := c.signedIn("zoe", "scholar-password")

	s.postForm(c.cls, url.Values{"body": {"oops"}})
	mine := lastMessage(t, c.env, c.cls)
	m.postForm(c.cls, url.Values{"body": {"from the mentor"}})
	theirs := lastMessage(t, c.env, c.cls)

	// Scholars remove their own messages only.
	expect(t, s.postJSON(fmt.Sprintf("%s/messages/%d/delete", c.cls, theirs.ID), nil), http.StatusForbidden)
	r := s.postJSON(fmt.Sprintf("%s/messages/%d/delete", c.cls, mine.ID), nil)
	expect(t, r, http.StatusOK, "Message removed by its author")
	expect(t, s.get(c.cls), http.StatusOK, "Message removed by its author")
	if strings.Contains(s.last, ">oops<") {
		t.Fatal("removed text must be gone")
	}

	// A class's mentor moderates its chat, but not the community channel.
	s.postForm(c.cls, url.Values{"body": {"rude"}})
	rude := lastMessage(t, c.env, c.cls)
	expect(t, m.get(c.cls), http.StatusOK, "Remove message", "Mute Sam")
	expectRedirect(t, m.postForm(fmt.Sprintf("%s/messages/%d/delete", c.cls, rude.ID), nil), c.cls)
	expect(t, s.get(c.cls), http.StatusOK, "Message removed by a moderator")
	z.postForm(c.com, url.Values{"body": {"community post"}})
	zp := lastMessage(t, c.env, c.com)
	expect(t, m.postJSON(fmt.Sprintf("%s/messages/%d/delete", c.com, zp.ID), nil), http.StatusForbidden)
	expect(t, m.postForm(c.com+"/mute", url.Values{"user": {fmt.Sprint(c.zoe.ID)}, "for": {"hour"}}), http.StatusForbidden)

	// Muting: they can read but not post, until unmuted.
	expectRedirect(t, m.postForm(c.cls+"/mute", url.Values{"user": {fmt.Sprint(c.sam.ID)}, "for": {"hour"}}), c.cls)
	expect(t, m.get(c.cls), http.StatusOK, "is muted here until", "Unmute")
	expect(t, s.get(c.cls), http.StatusOK, "You've been muted here until")
	if strings.Contains(s.last, `name="body"`) {
		t.Fatal("muted people get no message box")
	}
	expect(t, s.postJSON(c.cls, url.Values{"body": {"let me talk"}}), http.StatusForbidden, "muted")
	expect(t, s.postJSON(c.com, url.Values{"body": {"still fine here"}}), http.StatusOK) // only in that channel
	expectRedirect(t, m.postForm(c.cls+"/unmute", url.Values{"user": {fmt.Sprint(c.sam.ID)}}), c.cls)
	expect(t, s.postJSON(c.cls, url.Values{"body": {"thanks"}}), http.StatusOK)

	// People who lead a channel can't be muted in it.
	expectRedirect(t, a.postForm(c.cls+"/mute", url.Values{"user": {fmt.Sprint(c.mia.ID)}, "for": {"day"}}), c.cls)
	expect(t, a.get(c.cls), http.StatusOK, "can't be muted here")
	expectRedirect(t, a.postForm(c.com+"/mute", url.Values{"user": {fmt.Sprint(c.mia.ID)}, "for": {"always"}}), c.com)
	expect(t, m.get(c.com), http.StatusOK, "You've been muted here, so")
	expect(t, z.postForm(c.cls+"/mute", url.Values{"user": {fmt.Sprint(c.sam.ID)}, "for": {"hour"}}), http.StatusNotFound)

	// Admins turn channels off: hidden from scholars, read-only for mentors.
	expect(t, m.postForm(c.cls+"/enabled", url.Values{"on": {"0"}}), http.StatusForbidden)
	expectRedirect(t, a.postForm(c.cls+"/enabled", url.Values{"on": {"0"}}), c.cls)
	expect(t, s.get(c.cls), http.StatusNotFound)
	if strings.Contains(s.get("/chat").Body, "History of Liberty") {
		t.Fatal("a channel that's off is hidden from scholars")
	}
	expect(t, m.get(c.cls), http.StatusOK, "Chat is turned off here")
	expect(t, m.postJSON(c.cls, url.Values{"body": {"anyone?"}}), http.StatusForbidden)
	expectRedirect(t, a.postForm(c.cls+"/enabled", url.Values{"on": {"1"}}), c.cls)
	expect(t, s.get(c.cls), http.StatusOK)

	// Archived classes' chats are read-only.
	cl, _ := c.store.GetClass(c.class.ID)
	cl.Archived = true
	c.store.UpdateClass(cl)
	expect(t, s.get(c.cls), http.StatusOK, "archived, so its chat is read-only")
	expect(t, s.postJSON(c.cls, url.Values{"body": {"hello?"}}), http.StatusForbidden)
}

func TestChatThrottle(t *testing.T) {
	c := newChatEnv(t)
	s := c.signedIn("sam", "scholar-password")
	for i := 0; i < 20; i++ {
		expect(t, s.postJSON(c.com, url.Values{"body": {fmt.Sprint("message ", i)}}), http.StatusOK)
	}
	expect(t, s.postJSON(c.com, url.Values{"body": {"one too many"}}), http.StatusTooManyRequests, "very quickly")
}

// stream opens a channel's event stream and returns its events' data.
func stream(t *testing.T, b *browser, path, lastID string) (<-chan string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", b.url()+path+"/events", nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := b.c.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		cancel()
		t.Fatalf("stream: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	out := make(chan string, 16)
	go func() {
		defer res.Body.Close()
		defer close(out)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				out <- d
			}
		}
	}()
	return out, cancel
}

func next(t *testing.T, events <-chan string) string {
	t.Helper()
	select {
	case d, ok := <-events:
		if !ok {
			t.Fatal("stream ended")
		}
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return ""
}

func TestChatLive(t *testing.T) {
	c := newChatEnv(t)
	s := c.signedIn("sam", "scholar-password")
	m := c.signedIn("mia", "mentor-password")
	z := c.signedIn("zoe", "scholar-password")

	// Someone outside the class can't listen in.
	req, _ := http.NewRequest("GET", z.url()+c.cls+"/events", nil)
	if res, err := z.c.Do(req); err != nil || res.StatusCode != http.StatusNotFound {
		t.Fatalf("outsider's stream: %v %v", res.StatusCode, err)
	} else {
		res.Body.Close()
	}

	events, cancel := stream(t, s, c.cls, "")
	defer cancel()
	id := mustID(t, strings.TrimPrefix(c.cls, "/chat/"))
	eventually(t, "the stream to be listening", func() bool { return c.srv.chat.watchers(id) == 1 })

	m.postJSON(c.cls, url.Values{"body": {"Live hello"}})
	d := next(t, events)
	if !strings.Contains(d, "Live hello") || !strings.Contains(d, `"mine":false`) || strings.Contains(d, "Remove message") {
		t.Fatalf("event for Sam: %s", d)
	}
	first := lastMessage(t, c.env, c.cls)

	// Removing a message updates it.
	m.postJSON(fmt.Sprintf("%s/messages/%d/delete", c.cls, first.ID), nil)
	if d := next(t, events); !strings.Contains(d, "Message removed by its author") {
		t.Fatalf("removal event: %s", d)
	}

	// After a reconnect, missed messages come first.
	cancel()
	eventually(t, "the stream to close", func() bool { return c.srv.chat.watchers(id) == 0 })
	m.postJSON(c.cls, url.Values{"body": {"While you were away"}})
	events2, cancel2 := stream(t, s, c.cls, fmt.Sprint(first.ID))
	defer cancel2()
	if d := next(t, events2); !strings.Contains(d, "While you were away") {
		t.Fatalf("catch-up event: %s", d)
	}

	// Losing access ends the stream at the next check.
	c.srv.streamCheck.Store(int64(50 * time.Millisecond))
	events3, cancel3 := stream(t, s, c.cls, "")
	defer cancel3()
	c.store.RemoveMember(c.class.ID, c.sam.ID)
	select {
	case _, ok := <-events3:
		for ok {
			_, ok = <-events3
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream kept going after Sam left the class")
	}
}
