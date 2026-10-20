package server

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/bradyloveland/taper/internal/store"
	"github.com/bradyloveland/taper/internal/webpush"
)

// pushService is a fake push service that records what it's sent.
type pushService struct {
	*httptest.Server
	mu     sync.Mutex
	got    map[string][][]byte // by path
	status int
}

func newPushService(t *testing.T) *pushService {
	p := &pushService{got: map[string][][]byte{}, status: http.StatusCreated}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		defer p.mu.Unlock()
		if !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		p.got[r.URL.Path] = append(p.got[r.URL.Path], body)
		w.WriteHeader(p.status)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *pushService) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.got[path])
}

// pushDevice is a browser's side of a subscription.
type pushDevice struct {
	key  *ecdh.PrivateKey
	auth []byte
	path string
}

func newPushDevice(t *testing.T, path string) *pushDevice {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return &pushDevice{key: k, auth: auth, path: path}
}

func (d *pushDevice) form(base string) url.Values {
	b := base64.RawURLEncoding
	return url.Values{"endpoint": {base + d.path}, "p256dh": {b.EncodeToString(d.key.PublicKey().Bytes())}, "auth": {b.EncodeToString(d.auth)}}
}

func (d *pushDevice) read(t *testing.T, body []byte) pushPayload {
	t.Helper()
	plain, err := webpush.Decrypt(body, d.key, d.auth)
	if err != nil {
		t.Fatal(err)
	}
	var p pushPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPush(t *testing.T) {
	ps := newPushService(t)
	c := newCalEnvWith(t, func(o *Options) {
		o.PushClient = ps.Client()
		o.PushEndpointOK = func(ep string) bool { return strings.HasPrefix(ep, ps.URL+"/") }
	})
	s := c.signedIn("sam", "scholar-password")
	phone := newPushDevice(t, "/sam-phone")

	// The key, then subscribing this pushDevice.
	expect(t, s.get("/push/key"), http.StatusOK, `"key":"B`)
	expect(t, s.postForm("/push/subscribe", url.Values{"endpoint": {"https://evil.example/x"}, "p256dh": {"x"}, "auth": {"y"}}),
		http.StatusBadRequest, "push service isn")
	bad := phone.form(ps.URL)
	bad.Set("endpoint", "http://10.0.0.1/x")
	expect(t, s.postForm("/push/subscribe", bad), http.StatusBadRequest)
	expect(t, s.postForm("/push/subscribe", phone.form(ps.URL)), http.StatusOK, "on for this device")
	expect(t, s.get("/account"), http.StatusOK, "data-push hidden", "Turn on for this device")

	// A notification reaches the pushDevice, encrypted for it.
	m := c.signedIn("mia", "mentor-password")
	assignmentID(t, m.postFiles(fmt.Sprintf("/classes/%d/assignments/new", c.class.ID), url.Values{"title": {"Essay"}, "publish": {"now"}}, nil))
	c.srv.wg.Wait()
	if ps.count("/sam-phone") != 1 {
		t.Fatalf("pushes: %d", ps.count("/sam-phone"))
	}
	p := phone.read(t, ps.got["/sam-phone"][0])
	list, _ := c.store.ListNotifications(c.sam.ID, 1)
	if p.Title != "Liberty Commonwealth" || p.Body != "New assignment in History of Liberty: Essay" ||
		p.URL != fmt.Sprintf("/notifications/%d", list[0].ID) || p.Unread != 1 || !strings.HasPrefix(p.Icon, "/static/") {
		t.Fatalf("payload: %+v", p)
	}

	// The test button.
	expect(t, s.postForm("/push/test", nil), http.StatusOK, "Sent")
	if p := phone.read(t, ps.got["/sam-phone"][1]); !strings.Contains(p.Body, "This is a test") {
		t.Fatalf("test: %+v", p)
	}
	expect(t, m.postForm("/push/test", nil), http.StatusConflict, "Turn on notifications for this device first")

	// Signing out of the pushDevice stops its notifications.
	expectRedirect(t, s.postForm("/logout", nil), "/login")
	if subs, _ := c.store.PushSubsFor(c.sam.ID); len(subs) != 0 {
		t.Fatalf("still subscribed after signing out: %d", len(subs))
	}

	// Signing in again and saving it moves it to the new session; a push service saying it's gone forgets it.
	s = c.signedIn("sam", "scholar-password")
	expect(t, s.postForm("/push/subscribe", phone.form(ps.URL)), http.StatusOK)
	ps.mu.Lock()
	ps.status = http.StatusGone
	ps.mu.Unlock()
	c.signedIn("zoe", "scholar-password")
	c.store.AddMembers(c.class.ID, store.ClassScholar, []int64{c.zoe.ID})
	c.signedIn("zoe", "scholar-password").postJSON(mustChannel(t, c), url.Values{"body": {"hi"}})
	c.srv.wg.Wait()
	if subs, _ := c.store.PushSubsFor(c.sam.ID); len(subs) != 0 {
		t.Fatal("a gone subscription should be forgotten")
	}

	// Turning it off.
	expect(t, s.postForm("/push/subscribe", phone.form(ps.URL)), http.StatusOK)
	expect(t, s.postForm("/push/unsubscribe", url.Values{"endpoint": {ps.URL + "/sam-phone"}}), http.StatusOK)
	if subs, _ := c.store.PushSubsFor(c.sam.ID); len(subs) != 0 {
		t.Fatal("unsubscribed")
	}
}

func mustChannel(t *testing.T, c *calEnv) string {
	t.Helper()
	ch, err := c.store.ClassChannel(c.class.ID)
	if err != nil {
		t.Fatal(err)
	}
	return chatPath(ch.ID)
}
