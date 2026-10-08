package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bradyloveland/taper/internal/cal"
	"github.com/bradyloveland/taper/internal/store"
)

// Chat has a community channel for the whole school and one for each class.
// Pages are rendered on the server and work without JavaScript; with it, new
// messages arrive live over server-sent events.

const (
	maxChatMessage = 2000 // characters
	chatPage       = 60   // messages shown at a time
)

// ------------------------------------------------------------------- hub

// chatEvent tells the people watching a channel that a message is new or
// has changed (been removed).
type chatEvent struct {
	ID  int64
	New bool
}

// chatHub passes chat events to the open streams.
type chatHub struct {
	mu   sync.Mutex
	subs map[int64]map[chan chatEvent]struct{}
}

func newChatHub() *chatHub { return &chatHub{subs: map[int64]map[chan chatEvent]struct{}{}} }

func (h *chatHub) subscribe(channelID int64) chan chatEvent {
	c := make(chan chatEvent, 64)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[channelID] == nil {
		h.subs[channelID] = map[chan chatEvent]struct{}{}
	}
	h.subs[channelID][c] = struct{}{}
	return c
}

func (h *chatHub) unsubscribe(channelID int64, c chan chatEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[channelID][c]; ok {
		delete(h.subs[channelID], c)
		close(c)
	}
}

// publish sends an event to everyone watching. A stream that has fallen far
// behind is closed; the browser reconnects and catches up.
func (h *chatHub) publish(channelID int64, ev chatEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs[channelID] {
		select {
		case c <- ev:
		default:
			delete(h.subs[channelID], c)
			close(c)
		}
	}
}

// watchers counts open streams on a channel (for tests).
func (h *chatHub) watchers(channelID int64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[channelID])
}

// ---------------------------------------------------------------- access

// chanAccess is what the signed-in person may do in a channel.
type chanAccess struct {
	Ch       *store.Channel
	Name     string
	Color    string
	Link     string // the class page, for class channels
	See      bool
	Post     bool
	Moderate bool // remove anyone's messages and mute people: admins, and a class's mentors
	Manage   bool // turn the channel on and off: admins
	Mute     *store.Mute
	Why      string // why they can't post
}

func (s *Server) chanAccess(u *store.User, ch *store.Channel) (chanAccess, error) {
	a := chanAccess{Ch: ch, Manage: u.IsAdmin()}
	archived := false
	if ch.ClassID == 0 {
		a.Name, a.Color = "Community", "school"
		a.Moderate = u.IsAdmin()
		a.See = ch.Enabled || a.Moderate
	} else {
		role, err := s.store.ClassRole(ch.ClassID, u.ID)
		if err != nil {
			return a, err
		}
		a.Name, a.Color, a.Link = ch.ClassName, ch.ClassColor, classPath(ch.ClassID)
		a.Moderate = u.IsAdmin() || role == store.ClassMentor
		a.See = (role != "" || u.IsAdmin()) && (ch.Enabled || a.Moderate)
		archived = ch.ClassArchived
	}
	if !a.See {
		return a, nil
	}
	switch {
	case !ch.Enabled:
		a.Why = "Chat is turned off here."
	case archived:
		a.Why = "This class is archived, so its chat is read-only."
	default:
		if !a.Moderate {
			mu, err := s.store.MuteOf(ch.ID, u.ID)
			if err != nil {
				return a, err
			}
			if mu != nil {
				a.Mute = mu
				a.Why = "You've been muted here" + muteEnd(mu, s.loc()) + ", so you can read but not post."
				return a, nil
			}
		}
		a.Post = true
	}
	return a, nil
}

func muteEnd(mu *store.Mute, loc *time.Location) string {
	if mu.Until == 0 {
		return ""
	}
	return " until " + time.Unix(mu.Until, 0).In(loc).Format("Mon, Jan 2 at 3:04 PM")
}

// channel loads the channel in the URL if the signed-in person may see it.
func (s *Server) channel(w http.ResponseWriter, r *http.Request) (chanAccess, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return chanAccess{}, false
	}
	ch, err := s.store.GetChannel(id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return chanAccess{}, false
	}
	if err != nil {
		s.serverError(w, r, "loading channel", err)
		return chanAccess{}, false
	}
	a, err := s.chanAccess(current(r).user, ch)
	if err != nil {
		s.serverError(w, r, "checking channel access", err)
		return chanAccess{}, false
	}
	if !a.See {
		s.notFound(w, r)
		return chanAccess{}, false
	}
	return a, true
}

func chatPath(id int64) string { return "/chat/" + strconv.FormatInt(id, 10) }

// ---------------------------------------------------------------- lists

// chanItem is a channel in the list.
type chanItem struct {
	chanAccess
	Unread int
	Last   *store.Message
	Active bool
}

// myChannels lists the channels u can see: the community channel, then
// their current classes'. With create, missing class channels are made.
func (s *Server) myChannels(u *store.User, create bool) ([]chanItem, error) {
	var list []*store.Channel
	if ch, err := s.store.CommunityChannel(); err == nil {
		list = append(list, ch)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	classes, err := s.store.ListClassesFor(u.ID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, c := range classes {
		if !c.Archived {
			ids = append(ids, c.ID)
		}
	}
	if create {
		for _, id := range ids {
			ch, err := s.store.ClassChannel(id)
			if err != nil {
				return nil, err
			}
			list = append(list, ch)
		}
	} else {
		have, err := s.store.ClassChannels(ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if ch := have[id]; ch != nil {
				list = append(list, ch)
			}
		}
	}
	var out []chanItem
	var chIDs []int64
	for _, ch := range list {
		a, err := s.chanAccess(u, ch)
		if err != nil {
			return nil, err
		}
		if a.See {
			out = append(out, chanItem{chanAccess: a})
			chIDs = append(chIDs, ch.ID)
		}
	}
	unread, err := s.store.UnreadCounts(u.ID, chIDs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Unread = unread[out[i].Ch.ID]
	}
	return out, nil
}

// chatUnread counts unread messages in u's channels, for the menu.
func (s *Server) chatUnread(u *store.User) int {
	list, err := s.myChannels(u, false)
	if err != nil {
		slog.Error("counting unread messages", "err", err)
		return 0
	}
	n := 0
	for _, it := range list {
		if it.Ch.Enabled {
			n += it.Unread
		}
	}
	return n
}

type chatListData struct {
	Channels []chanItem
}

func (s *Server) handleChatList(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	list, err := s.myChannels(u, true)
	if err != nil {
		s.serverError(w, r, "listing channels", err)
		return
	}
	s.fillLatest(list)
	s.render(w, r, http.StatusOK, "chat", "Chat", "chat", chatListData{Channels: list})
}

func (s *Server) fillLatest(list []chanItem) {
	var ids []int64
	for _, it := range list {
		ids = append(ids, it.Ch.ID)
	}
	latest, err := s.store.LatestMessages(ids)
	if err != nil {
		slog.Error("loading latest messages", "err", err)
		return
	}
	for i := range list {
		list[i].Last = latest[list[i].Ch.ID]
	}
}

func (s *Server) handleChatUnread(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int{"total": s.chatUnread(current(r).user)})
}

// ------------------------------------------------------------- messages

// msgView is a message as shown to one person.
type msgView struct {
	M         *store.Message
	ChannelID int64
	CSRF      string
	Body      template.HTML
	Initials  string
	Badge     string // Mentor or Admin, for people who lead the channel
	Time      string
	Stamp     string // full date and time, for the tooltip
	DayKey    string // YYYY-MM-DD at the school
	DayLabel  string // Today, Yesterday, Monday, October 5
	NewDay    bool   // the first message of its day in the list
	Cont      bool   // follows a message by the same person shortly before
	Mine      bool
	CanDelete bool
	CanMute   bool
	FirstName string
	Removed   string // who removed it
}

// msgViewer renders messages for one person in one channel, remembering
// who leads the channel.
type msgViewer struct {
	s       *Server
	u       *store.User
	a       chanAccess
	csrf    string
	loc     *time.Location
	today   time.Time
	leaders map[int64]bool
}

func (s *Server) viewer(r *http.Request, a chanAccess) *msgViewer {
	ri := current(r)
	return &msgViewer{s: s, u: ri.user, a: a, csrf: ri.sess.CSRF, loc: s.loc(), today: s.today(), leaders: map[int64]bool{}}
}

// leads reports whether someone leads the channel (can't be muted, and gets
// a badge): admins, and in a class channel its mentors.
func (v *msgViewer) leads(m *store.Message) bool {
	if m.AuthorRole == store.RoleAdmin {
		return true
	}
	if v.a.Ch.ClassID == 0 || m.UserID == 0 {
		return false
	}
	l, ok := v.leaders[m.UserID]
	if !ok {
		role, _ := v.s.store.ClassRole(v.a.Ch.ClassID, m.UserID)
		l = role == store.ClassMentor
		v.leaders[m.UserID] = l
	}
	return l
}

func (v *msgViewer) view(m *store.Message) msgView {
	t := time.Unix(m.CreatedAt, 0).In(v.loc)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	mv := msgView{M: m, ChannelID: v.a.Ch.ID, CSRF: v.csrf, Initials: (&store.User{DisplayName: m.AuthorName}).Initials(),
		Time: t.Format("3:04 PM"), Stamp: t.Format("Monday, January 2, 2006 at 3:04 PM"), DayKey: cal.FormatDate(day),
		Mine: m.UserID == v.u.ID}
	switch {
	case day.Equal(v.today):
		mv.DayLabel = "Today"
	case day.Equal(v.today.AddDate(0, 0, -1)):
		mv.DayLabel = "Yesterday"
	case day.Year() == v.today.Year():
		mv.DayLabel = day.Format("Monday, January 2")
	default:
		mv.DayLabel = day.Format("Monday, January 2, 2006")
	}
	leads := v.leads(m)
	if leads {
		mv.Badge = store.RoleLabel(m.AuthorRole)
		if m.AuthorRole != store.RoleAdmin {
			mv.Badge = "Mentor"
		}
	}
	if m.Deleted() {
		if m.DeletedBy == m.UserID {
			mv.Removed = "its author"
		} else {
			mv.Removed = "a moderator"
		}
		return mv
	}
	mv.Body = chatHTML(m.Body)
	mv.CanDelete = mv.Mine || v.a.Moderate
	mv.CanMute = v.a.Moderate && !mv.Mine && !leads && m.UserID != 0
	mv.FirstName = strings.Fields(m.AuthorName + " x")[0]
	return mv
}

// views renders a list, marking new days and runs by the same person.
func (v *msgViewer) views(list []*store.Message) []msgView {
	out := make([]msgView, len(list))
	for i, m := range list {
		out[i] = v.view(m)
		if i == 0 {
			out[i].NewDay = true
			continue
		}
		prev := out[i-1]
		out[i].NewDay = prev.DayKey != out[i].DayKey
		out[i].Cont = !out[i].NewDay && prev.M.UserID == m.UserID && !prev.M.Deleted() && !m.Deleted() &&
			m.CreatedAt-prev.M.CreatedAt < 5*60
	}
	return out
}

// fragment renders one message on its own, for live updates.
func (s *Server) fragment(v msgView) (string, error) {
	var buf bytes.Buffer
	err := s.pages["chat-channel"].ExecuteTemplate(&buf, "chat-message", v)
	return buf.String(), err
}

var linkRE = regexp.MustCompile(`https?://[^\s<>"']+`)

// chatHTML escapes a message, turns web addresses into links and keeps line
// breaks.
func chatHTML(body string) template.HTML {
	var b strings.Builder
	last := 0
	for _, loc := range linkRE.FindAllStringIndex(body, -1) {
		start, end := loc[0], loc[1]
		// Leave trailing punctuation out of the link.
		for end > start && strings.ContainsRune(".,;:!?)]}'", rune(body[end-1])) {
			end--
		}
		b.WriteString(template.HTMLEscapeString(body[last:start]))
		u := template.HTMLEscapeString(body[start:end])
		b.WriteString(`<a href="` + u + `" rel="noopener noreferrer nofollow" target="_blank">` + u + `</a>`)
		last = end
	}
	b.WriteString(template.HTMLEscapeString(body[last:]))
	return template.HTML(strings.ReplaceAll(b.String(), "\n", "<br>"))
}

// ---------------------------------------------------------- a channel

type channelData struct {
	A        chanAccess
	Channels []chanItem
	Messages []msgView
	More     bool  // there are earlier messages
	Before   int64 // showing messages before this one
	Mutes    []*store.Mute
	Draft    string
	Error    string
	LastID   int64
	Me       int64
}

func (s *Server) handleChannel(w http.ResponseWriter, r *http.Request) {
	a, ok := s.channel(w, r)
	if !ok {
		return
	}
	s.renderChannel(w, r, http.StatusOK, a, "", "")
}

func (s *Server) renderChannel(w http.ResponseWriter, r *http.Request, status int, a chanAccess, draft, msg string) {
	u := current(r).user
	d := channelData{A: a, Draft: draft, Error: msg, Me: u.ID}
	d.Before, _ = strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	list, more, err := s.store.ListMessages(a.Ch.ID, d.Before, chatPage)
	if err != nil {
		s.serverError(w, r, "listing messages", err)
		return
	}
	d.More = more
	d.Messages = s.viewer(r, a).views(list)
	if len(list) > 0 {
		d.LastID = list[len(list)-1].ID
		if d.Before == 0 {
			if err := s.store.MarkRead(u.ID, a.Ch.ID, d.LastID); err != nil {
				s.logError(r, "marking read", err)
			}
		}
	}
	if d.Channels, err = s.myChannels(u, true); err != nil {
		s.serverError(w, r, "listing channels", err)
		return
	}
	found := false
	for i := range d.Channels {
		d.Channels[i].Active = d.Channels[i].Ch.ID == a.Ch.ID
		found = found || d.Channels[i].Active
	}
	if !found { // an admin looking in on a class they're not in
		d.Channels = append(d.Channels, chanItem{chanAccess: a, Active: true})
	}
	if a.Moderate {
		d.Mutes, _ = s.store.ListMutes(a.Ch.ID)
	}
	s.render(w, r, status, "chat-channel", a.Name+" chat", "chat", d)
}

// wantsJSON reports whether the page's script sent the request.
func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// chatError answers a chat action that didn't work.
func (s *Server) chatError(w http.ResponseWriter, r *http.Request, a chanAccess, status int, draft, msg string) {
	if wantsJSON(r) {
		writeJSON(w, status, map[string]string{"error": msg})
		return
	}
	s.renderChannel(w, r, status, a, draft, msg)
}

func (s *Server) handleChatPost(w http.ResponseWriter, r *http.Request) {
	a, ok := s.channel(w, r)
	if !ok {
		return
	}
	u := current(r).user
	body := strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"))
	switch {
	case !a.Post:
		s.chatError(w, r, a, http.StatusForbidden, body, a.Why)
		return
	case body == "":
		s.chatError(w, r, a, http.StatusUnprocessableEntity, "", "Write a message first.")
		return
	case utf8.RuneCountInString(body) > maxChatMessage:
		s.chatError(w, r, a, http.StatusUnprocessableEntity, body, fmt.Sprintf("That's too long for chat. Keep messages to %d characters.", maxChatMessage))
		return
	}
	key := strconv.FormatInt(u.ID, 10)
	if s.chatThrottle.Blocked(key) > 0 {
		s.chatError(w, r, a, http.StatusTooManyRequests, body, "You're sending messages very quickly. Wait a moment, then try again.")
		return
	}
	s.chatThrottle.Fail(key)
	m, err := s.store.PostMessage(a.Ch.ID, u.ID, body)
	if err != nil {
		s.serverError(w, r, "posting message", err)
		return
	}
	_ = s.store.MarkRead(u.ID, a.Ch.ID, m.ID)
	s.chat.publish(a.Ch.ID, chatEvent{ID: m.ID, New: true})
	if wantsJSON(r) {
		html, err := s.fragment(s.viewer(r, a).view(m))
		if err != nil {
			s.logError(r, "rendering message", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": m.ID, "html": html})
		return
	}
	http.Redirect(w, r, chatPath(a.Ch.ID)+"#m"+strconv.FormatInt(m.ID, 10), http.StatusSeeOther)
}

func (s *Server) handleChatDelete(w http.ResponseWriter, r *http.Request) {
	a, ok := s.channel(w, r)
	if !ok {
		return
	}
	u := current(r).user
	mid, err := strconv.ParseInt(r.PathValue("mid"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	m, err := s.store.GetMessage(mid)
	if err != nil || m.ChannelID != a.Ch.ID {
		s.notFound(w, r)
		return
	}
	if m.UserID != u.ID && !a.Moderate {
		s.chatError(w, r, a, http.StatusForbidden, "", "You can only remove your own messages.")
		return
	}
	if err := s.store.DeleteMessage(m.ID, u.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, "removing message", err)
		return
	}
	if m.UserID != u.ID {
		slog.Info("chat message removed", "by", u.Username, "channel", a.Ch.ID, "message", m.ID)
	}
	s.chat.publish(a.Ch.ID, chatEvent{ID: m.ID})
	if wantsJSON(r) {
		m, _ = s.store.GetMessage(m.ID)
		html, _ := s.fragment(s.viewer(r, a).view(m))
		writeJSON(w, http.StatusOK, map[string]any{"id": m.ID, "html": html})
		return
	}
	http.Redirect(w, r, chatPath(a.Ch.ID), http.StatusSeeOther)
}

// muteFor is how long each choice mutes someone for (0: until unmuted).
var muteFor = map[string]time.Duration{"hour": time.Hour, "day": 24 * time.Hour, "week": 7 * 24 * time.Hour, "always": 0}

func (s *Server) handleChatMute(w http.ResponseWriter, r *http.Request) {
	a, ok := s.channel(w, r)
	if !ok {
		return
	}
	if !a.Moderate {
		s.renderError(w, r, http.StatusForbidden, "You can't mute people here", "Admins, and a class's mentors in its chat, can mute people.")
		return
	}
	u := current(r).user
	d, ok := muteFor[r.PostFormValue("for")]
	uid, err := strconv.ParseInt(r.PostFormValue("user"), 10, 64)
	if !ok || err != nil {
		s.notFound(w, r)
		return
	}
	target, err := s.store.GetUser(uid)
	if err != nil || target.ID == u.ID {
		s.notFound(w, r)
		return
	}
	// People who lead the channel can't be muted in it.
	if ta, err := s.chanAccess(target, a.Ch); err != nil || !ta.See || ta.Moderate {
		s.setFlash(w, r, "error", target.DisplayName+" can't be muted here.")
		http.Redirect(w, r, chatPath(a.Ch.ID), http.StatusSeeOther)
		return
	}
	var until int64
	if d > 0 {
		until = time.Now().Add(d).Unix()
	}
	if err := s.store.SetMute(a.Ch.ID, target.ID, until, u.ID); err != nil {
		s.serverError(w, r, "muting", err)
		return
	}
	slog.Info("muted in chat", "by", u.Username, "channel", a.Ch.ID, "who", target.Username, "for", r.PostFormValue("for"))
	mu, _ := s.store.MuteOf(a.Ch.ID, target.ID)
	end := ""
	if mu != nil {
		end = muteEnd(mu, s.loc())
	}
	s.redirect(w, r, chatPath(a.Ch.ID), target.DisplayName+" is muted here"+end+". They can still read the chat.")
}

func (s *Server) handleChatUnmute(w http.ResponseWriter, r *http.Request) {
	a, ok := s.channel(w, r)
	if !ok {
		return
	}
	if !a.Moderate {
		s.renderError(w, r, http.StatusForbidden, "You can't unmute people here", "Admins, and a class's mentors in its chat, can unmute people.")
		return
	}
	uid, err := strconv.ParseInt(r.PostFormValue("user"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := s.store.Unmute(a.Ch.ID, uid); err != nil {
		s.serverError(w, r, "unmuting", err)
		return
	}
	name := "They"
	if t, err := s.store.GetUser(uid); err == nil {
		name = t.DisplayName
	}
	s.redirect(w, r, chatPath(a.Ch.ID), name+" can post here again.")
}

func (s *Server) handleChatEnabled(w http.ResponseWriter, r *http.Request) {
	a, ok := s.channel(w, r)
	if !ok {
		return
	}
	if !a.Manage {
		s.renderError(w, r, http.StatusForbidden, "Admins only", "Only admins can turn chat on and off.")
		return
	}
	on := r.PostFormValue("on") == "1"
	if err := s.store.SetChannelEnabled(a.Ch.ID, on); err != nil {
		s.serverError(w, r, "changing channel", err)
		return
	}
	slog.Info("chat channel changed", "by", current(r).user.Username, "channel", a.Name, "on", on)
	msg := "Chat in " + a.Name + " is turned off. Messages are kept, and only admins and mentors can see them."
	if on {
		msg = "Chat in " + a.Name + " is turned on."
	}
	if a.Ch.ClassID == 0 && !on {
		msg = "Community chat is turned off. Messages are kept, and only admins can see them."
	}
	target := chatPath(a.Ch.ID)
	if back := r.PostFormValue("back"); back == "class" && a.Link != "" {
		target = a.Link
	}
	s.redirect(w, r, target, msg)
}

func (s *Server) handleChatRead(w http.ResponseWriter, r *http.Request) {
	a, ok := s.channel(w, r)
	if !ok {
		return
	}
	last, err := strconv.ParseInt(r.PostFormValue("last"), 10, 64)
	if err != nil || last <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad message"})
		return
	}
	if err := s.store.MarkRead(current(r).user.ID, a.Ch.ID, last); err != nil {
		s.logError(r, "marking read", err)
	}
	writeJSON(w, http.StatusOK, map[string]int{"total": s.chatUnread(current(r).user)})
}

// ------------------------------------------------------------- live updates

// defaultStreamCheck is how often an open stream checks that its viewer may
// still see the channel, and sends a keep-alive.
const defaultStreamCheck = 25 * time.Second

// handleChatEvents streams a channel's new and changed messages as
// server-sent events. Each event's data is the message rendered for the
// viewer. After a reconnect, missed messages are sent first.
func (s *Server) handleChatEvents(w http.ResponseWriter, r *http.Request) {
	a, ok := s.channel(w, r)
	if !ok {
		return
	}
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // ask nginx not to hold events back
	sub := s.chat.subscribe(a.Ch.ID)
	defer s.chat.unsubscribe(a.Ch.ID, sub)
	w.WriteHeader(http.StatusOK)

	v := s.viewer(r, a)
	send := func(m *store.Message, isNew bool) error {
		html, err := s.fragment(v.view(m))
		if err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"id": m.ID, "html": html, "mine": m.UserID == v.u.ID})
		if isNew {
			fmt.Fprintf(w, "id: %d\n", m.ID)
		}
		if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", data); err != nil {
			return err
		}
		return rc.Flush()
	}
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return
	}
	last, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if last == 0 {
		last, _ = strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	}
	if last > 0 {
		missed, err := s.store.MessagesAfter(a.Ch.ID, last, 200)
		if err != nil {
			s.logError(r, "catching up", err)
			return
		}
		for _, m := range missed {
			if send(m, true) != nil {
				return
			}
		}
	}
	if rc.Flush() != nil {
		return
	}
	cookie, _ := r.Cookie(sessionCookie)
	tick := time.NewTicker(time.Duration(s.streamCheck.Load()))
	defer tick.Stop()
	closing := closingFor(r)
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				return // fell behind; the browser reconnects and catches up
			}
			m, err := s.store.GetMessage(ev.ID)
			if err != nil {
				continue
			}
			if send(m, ev.New) != nil {
				return
			}
		case <-tick.C:
			// Stop if they've signed out, or lost access.
			if cookie == nil {
				return
			}
			if _, u, err := s.store.LookupSession(cookie.Value); err != nil || u.ID != v.u.ID {
				return
			}
			if ch, err := s.store.GetChannel(a.Ch.ID); err != nil {
				return
			} else if na, err := s.chanAccess(v.u, ch); err != nil || !na.See {
				return
			} else {
				v.a = na
			}
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-closing:
			return
		}
	}
}
