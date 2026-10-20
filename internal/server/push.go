package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/bradyloveland/taper/internal/store"
	"github.com/bradyloveland/taper/internal/version"
	"github.com/bradyloveland/taper/internal/webpush"
)

// Web push puts notifications on people's lock screens. Each person turns it
// on for each device (under My account), and it stays tied to the session
// that turned it on: signing out of a device stops its notifications. The
// message is encrypted for the device, so the push service in between
// (Apple, Google, Mozilla, Microsoft) can't read it.

// settingVAPID holds the server's push key (encrypted with secret.key).
const settingVAPID = "vapid_key"

const pushTTL = 24 * time.Hour

// pushKey returns the server's VAPID key, making one the first time.
func (s *Server) pushKey() (*webpush.Key, error) {
	s.pushMu.Lock()
	defer s.pushMu.Unlock()
	if s.vapid != nil {
		return s.vapid, nil
	}
	var sealed string
	if ok, err := s.store.GetSetting(settingVAPID, &sealed); err != nil {
		return nil, err
	} else if ok {
		raw, err := s.box.Open(sealed)
		if err != nil {
			return nil, err
		}
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, err
		}
		if s.vapid, err = webpush.ParseKey(b); err != nil {
			return nil, err
		}
		return s.vapid, nil
	}
	b, err := webpush.GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := s.store.SetSetting(settingVAPID, s.box.Seal(base64.StdEncoding.EncodeToString(b))); err != nil {
		return nil, err
	}
	s.vapid, err = webpush.ParseKey(b)
	return s.vapid, err
}

// pushSubject is the contact push services see: the school's address.
func (s *Server) pushSubject() string {
	if u := s.publicURL(); u != "" {
		return u
	}
	return "https://github.com/" + version.Repo
}

// pushPayload is what the service worker shows.
type pushPayload struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	Tag    string `json:"tag"`
	Icon   string `json:"icon"`
	Unread int    `json:"unread"` // for the badge on the app's icon
}

// pushEach sends notifications to their people's devices, in the background.
func (s *Server) pushEach(list []*store.Notification) {
	if len(list) == 0 {
		return
	}
	byUser := map[int64][]*store.Notification{}
	for _, n := range list {
		byUser[n.UserID] = append(byUser[n.UserID], n)
	}
	type job struct {
		sub *store.PushSub
		msg webpush.Message
	}
	var jobs []job
	school, _ := s.appNames()
	icon := s.assets.url("icons/icon-192.png")
	for uid, ns := range byUser {
		subs, err := s.store.PushSubsFor(uid)
		if err != nil {
			slog.Error("listing devices", "err", err)
			continue
		}
		if len(subs) == 0 {
			continue
		}
		unread, _ := s.store.UnreadNotifications(uid)
		for _, n := range ns {
			tag := n.Ref
			if tag == "" {
				tag = "n" + strconv.FormatInt(n.ID, 10)
			}
			body, _ := json.Marshal(pushPayload{Title: school, Body: n.Title, URL: "/notifications/" + strconv.FormatInt(n.ID, 10),
				Tag: tag, Icon: icon, Unread: unread})
			for _, sub := range subs {
				jobs = append(jobs, job{sub, webpush.Message{Payload: body, TTL: pushTTL, Topic: topic(tag), Subject: s.pushSubject()}})
			}
		}
	}
	if len(jobs) == 0 {
		return
	}
	key, err := s.pushKey()
	if err != nil {
		slog.Error("loading the push key", "err", err)
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, j := range jobs {
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer func() { <-sem; wg.Done() }()
				s.pushOne(key, j.sub, j.msg)
			}()
		}
		wg.Wait()
	}()
}

// pushOne delivers one message, and forgets devices that have gone.
func (s *Server) pushOne(key *webpush.Key, sub *store.PushSub, m webpush.Message) {
	p256dh, err1 := webpush.Decode(sub.P256dh)
	auth, err2 := webpush.Decode(sub.Auth)
	if err1 != nil || err2 != nil {
		_ = s.store.DeletePushSubByID(sub.ID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := webpush.Send(ctx, s.pushClient, key, webpush.Subscription{Endpoint: sub.Endpoint, P256dh: p256dh, Auth: auth}, m)
	switch {
	case errors.Is(err, webpush.ErrGone):
		_ = s.store.DeletePushSubByID(sub.ID)
	case err != nil:
		slog.Warn("sending a push", "err", err)
		_ = s.store.PushResult(sub.ID, false)
	default:
		_ = s.store.PushResult(sub.ID, true)
	}
}

// topic makes a Topic header (up to 32 base64url characters) from a tag, so a
// newer message about the same thing replaces one still waiting.
func topic(tag string) string {
	out := make([]byte, 0, 32)
	for i := 0; i < len(tag) && len(out) < 32; i++ {
		c := tag[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			out = append(out, c)
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}

// ---------------------------------------------------------------- handlers

// handlePushKey gives the page the public key to subscribe with.
func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.pushKey()
	if err != nil {
		s.logError(r, "loading the push key", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Notifications on devices aren't working on this server."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key.Public()})
}

// handlePushSubscribe saves this device's subscription for the session.
func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	ri := current(r)
	endpoint := r.PostFormValue("endpoint")
	p256dh, err1 := webpush.Decode(r.PostFormValue("p256dh"))
	auth, err2 := webpush.Decode(r.PostFormValue("auth"))
	if err1 != nil || err2 != nil || len(p256dh) != 65 || len(auth) != 16 || len(endpoint) > 1000 || !s.pushEndpointOK(endpoint) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "This browser's push service isn't one Taper knows."})
		return
	}
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	sub := &store.PushSub{UserID: ri.user.ID, SessionHash: ri.sess.TokenHash, Endpoint: endpoint,
		P256dh: webpushB64(p256dh), Auth: webpushB64(auth), UserAgent: ua}
	if err := s.store.SavePushSub(sub); err != nil {
		s.logError(r, "saving a device", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Couldn't save this device. Try again."})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "Notifications are on for this device."})
}

// handlePushUnsubscribe forgets this device.
func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeletePushSub(current(r).user.ID, r.PostFormValue("endpoint")); err != nil {
		s.logError(r, "forgetting a device", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "Notifications are off for this device."})
}

// handlePushTest sends a test notification to the person's devices.
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	subs, err := s.store.PushSubsFor(u.ID)
	if err != nil || len(subs) == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Turn on notifications for this device first."})
		return
	}
	key, err := s.pushKey()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Notifications on devices aren't working on this server."})
		return
	}
	school, _ := s.appNames()
	body, _ := json.Marshal(pushPayload{Title: school, Body: "This is a test. Notifications work on this device.", URL: "/notifications",
		Tag: "test", Icon: s.assets.url("icons/icon-192.png")})
	for _, sub := range subs {
		s.pushOne(key, sub, webpush.Message{Payload: body, TTL: time.Hour, Topic: "test", Subject: s.pushSubject()})
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "Sent. It should appear in a moment on " + plural(len(subs), "device") + "."})
}

func webpushB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
