package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bradyloveland/taper/internal/cal"
	"github.com/bradyloveland/taper/internal/store"
)

// Notifications tell people about things that concern them: in the app (the
// bell in the top bar), and by email for people who choose it, either one
// email each or a summary each evening. Email only goes to people who turned
// it on, and says what happened without the content (no message text or
// feedback), since scholars are often minors.

const (
	dueReminderHour = 15 // reminders about work due tomorrow go from 3 PM
	digestHour      = 18 // daily summaries go from 6 PM
	noticeKeep      = 60 * 24 * time.Hour
)

// notify sends n to the people, except the one who caused it, in the
// background of the request, and emails those who chose an email for each.
func (s *Server) notify(n store.Notice, users []int64, except int64) {
	seen := map[int64]bool{except: true}
	var to []int64
	for _, id := range users {
		if !seen[id] {
			seen[id] = true
			to = append(to, id)
		}
	}
	emails, changed, err := s.store.Notify(n, to)
	if err != nil {
		slog.Error("saving notifications", "kind", n.Kind, "err", err)
		return
	}
	s.emailEach(emails)
	s.pushEach(changed)
}

// emailEach emails notifications one by one, in the background.
func (s *Server) emailEach(list []*store.Notification) {
	if len(list) == 0 {
		return
	}
	ids := make([]int64, len(list))
	for i, n := range list {
		ids[i] = n.ID
	}
	// Recorded as sent either way, so a broken mail server doesn't build up a queue.
	if err := s.store.MarkEmailed(ids); err != nil {
		slog.Error("marking notifications emailed", "err", err)
	}
	if !s.emailReady() {
		return
	}
	school, _ := s.appNames()
	base := s.publicURL()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for _, n := range list {
			u, err := s.store.GetUser(n.UserID)
			if err != nil || !u.Active || u.Email == "" {
				continue
			}
			body := n.Title + "."
			if base != "" && n.Link != "" {
				body += "\n\n" + base + n.Link
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			err = s.sendMail(ctx, []string{u.Email}, n.Title, body+emailFooter(school, false))
			cancel()
			if err != nil {
				slog.Warn("emailing notification", "user", u.Username, "err", err)
			}
		}
	}()
}

func emailFooter(school string, daily bool) string {
	how := "an email for each notification"
	if daily {
		how = "a daily summary of notifications"
	}
	return "\n\n-- \nTaper at " + school + "\nYou chose " + how + ". Change that under My account → Notifications.\n"
}

// --------------------------------------------------------------- events

// announceAssignments tells scholars and their parents about assignments
// that are now published and nobody has heard of yet.
func (s *Server) announceAssignments(now time.Time) {
	list, err := s.store.UnannouncedAssignments(now.Unix())
	if err != nil {
		slog.Error("listing new assignments", "err", err)
		return
	}
	for _, a := range list {
		if ok, err := s.store.MarkAnnounced(a.ID); err != nil || !ok {
			continue
		}
		people, err := s.store.ClassAudience(a.ClassID)
		if err != nil {
			slog.Error("listing class", "err", err)
			continue
		}
		mentors, _ := s.store.ClassMentorIDs(a.ClassID)
		skip := map[int64]bool{}
		for _, m := range mentors {
			skip[m] = true
		}
		var to []int64
		for _, id := range people {
			if !skip[id] {
				to = append(to, id)
			}
		}
		title := "New assignment in " + a.ClassName + ": " + a.Title
		if d := dueText(a, s.loc(), s.today()); a.DueDate != "" {
			title += " (" + strings.ToLower(d[:1]) + d[1:] + ")"
		}
		s.notify(store.Notice{Kind: store.NotifyAssignment, Ref: "assignment:" + strconv.FormatInt(a.ID, 10), Title: title,
			Link: assignmentPath(a.ID)}, to, a.CreatedBy)
	}
}

// notifyTurnedIn tells a class's mentors that work is waiting for them.
func (s *Server) notifyTurnedIn(a *store.Assignment, sch *store.User, by int64) {
	mentors, err := s.store.ClassMentorIDs(a.ClassID)
	if err != nil {
		slog.Error("listing mentors", "err", err)
		return
	}
	s.notify(store.Notice{Kind: store.NotifyWork, Ref: "review:" + strconv.FormatInt(a.ID, 10),
		Title: sch.DisplayName + " turned in " + a.Title,
		Many:  "{n} pieces of work to review for " + a.Title,
		Link:  assignmentPath(a.ID) + "#roster"}, mentors, by)
}

// notifyScholar tells a scholar, and their parents, about their work. what
// finishes "Essay …", like "needs another look".
func (s *Server) notifyScholar(kind string, a *store.Assignment, sch *store.User, what string, by int64) {
	link := assignmentPath(a.ID) + "#work"
	s.notify(store.Notice{Kind: kind, Title: a.Title + " " + what, Link: link}, []int64{sch.ID}, by)
	parents, err := s.store.ParentIDs(sch.ID)
	if err != nil {
		slog.Error("listing parents", "err", err)
		return
	}
	s.notify(store.Notice{Kind: kind, Title: sch.DisplayName + "'s " + a.Title + " " + what,
		Link: assignmentPath(a.ID) + "?scholar=" + strconv.FormatInt(sch.ID, 10) + "#work"}, parents, by)
}

// remindDue tells scholars, and their parents, about work due tomorrow that
// they haven't turned in, once each, from the afternoon before.
func (s *Server) remindDue(now time.Time) {
	local := now.In(s.loc())
	if local.Hour() < dueReminderHour {
		return
	}
	today := cal.Today(s.loc(), now)
	list, err := s.store.DueOn(cal.FormatDate(today.AddDate(0, 0, 1)), now.Unix())
	if err != nil {
		slog.Error("listing work due", "err", err)
		return
	}
	for _, a := range list {
		scholars, err := s.store.ClassScholarIDs(a.ClassID)
		if err != nil {
			continue
		}
		work, _ := s.store.ListSubmissions(a.ID)
		for _, id := range scholars {
			if wk := work[id]; wk != nil && wk.Status != store.WorkDraft && wk.Status != store.WorkNeedsWork {
				continue
			}
			if first, err := s.store.Once(fmt.Sprintf("due:%d:%d", a.ID, id)); err != nil || !first {
				continue
			}
			sch, err := s.store.GetUser(id)
			if err != nil {
				continue
			}
			s.notifyScholar(store.NotifyDue, a, sch, "is due "+strings.TrimPrefix(dueText(a, s.loc(), today), "Due ")+" and isn't turned in yet", 0)
		}
	}
}

// notifyEvent tells the people who follow a calendar about a new, changed or
// cancelled event (or one date of it), unless it's all in the past.
func (s *Server) notifyEvent(e *store.CalEvent, what, date string, by int64) {
	today := cal.FormatDate(s.today())
	if date == "" {
		date = e.StartDate
		if e.Repeat != "" && date < today {
			date = ""
		}
	}
	if date != "" && date < today {
		return
	}
	if e.ClassID != 0 && e.ClassName == "" {
		if c, err := s.store.GetClass(e.ClassID); err == nil {
			e.ClassName = c.Name
		}
	}
	var people []int64
	var err error
	if e.ClassID == 0 {
		people, err = s.store.ActiveUserIDs()
	} else {
		people, err = s.store.ClassAudience(e.ClassID)
	}
	if err != nil {
		slog.Error("listing calendar people", "err", err)
		return
	}
	when := ""
	if d, err := cal.ParseDate(date); err == nil {
		when = " on " + d.Format("Mon, Jan 2")
	}
	where := "the school calendar"
	if e.ClassName != "" {
		where = e.ClassName
	}
	link := "/events/" + strconv.FormatInt(e.ID, 10)
	if what == "cancelled" {
		link = "/calendar"
	}
	s.notify(store.Notice{Kind: store.NotifyCalendar, Ref: "event:" + strconv.FormatInt(e.ID, 10),
		Title: capitalize(what) + " in " + where + ": " + e.Title + when, Link: link}, people, by)
}

// notifyMessage tells the people in a chat about a new message.
func (s *Server) notifyMessage(ch *store.Channel, name string, author *store.User) {
	if !ch.Enabled {
		return
	}
	var people []int64
	var err error
	switch ch.Kind {
	case store.ChannelCommunity:
		people, err = s.store.ActiveUserIDs()
	case store.ChannelGroup:
		people, err = s.store.ChannelMemberIDs(ch.ID)
	default:
		if ch.ClassArchived {
			return
		}
		people, err = s.store.ClassAudience(ch.ClassID)
	}
	if err != nil {
		slog.Error("listing chat people", "err", err)
		return
	}
	s.notify(store.Notice{Kind: store.NotifyChat, Ref: chatRef(ch.ID), Title: author.DisplayName + " wrote in " + name,
		Many: "{n} new messages in " + name, Link: chatPath(ch.ID)}, people, author.ID)
}

func chatRef(id int64) string { return "chat:" + strconv.FormatInt(id, 10) }

// ------------------------------------------------------------- background

// runNotices does the scheduled notification work every minute until ctx
// ends.
func (s *Server) runNotices(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.noticeTick(now)
		}
	}
}

// noticeTick announces assignments published since the last tick, sends
// reminders and daily summaries when it's time, and forgets old
// notifications.
func (s *Server) noticeTick(now time.Time) {
	s.announceAssignments(now)
	s.remindDue(now)
	s.sendDigests(now)
	if now.Minute() == 0 && now.Hour() == 3 {
		if err := s.store.PruneNotifications(now.Add(-noticeKeep).Unix()); err != nil {
			slog.Error("pruning notifications", "err", err)
		}
	}
}

// sendDigests emails the evening summary to people who chose one.
func (s *Server) sendDigests(now time.Time) {
	local := now.In(s.loc())
	if local.Hour() < digestHour || !s.emailReady() {
		return
	}
	since := time.Date(local.Year(), local.Month(), local.Day(), digestHour, 0, 0, 0, s.loc()).Unix()
	people, err := s.store.DigestDue(since)
	if err != nil {
		slog.Error("listing daily summaries", "err", err)
		return
	}
	school, _ := s.appNames()
	base := s.publicURL()
	for _, p := range people {
		u, err := s.store.GetUser(p.UserID)
		if err != nil {
			continue
		}
		list, err := s.store.DigestFor(p.UserID)
		if err != nil || len(list) == 0 || u.Email == "" {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Here's what happened at %s since your last summary:\n\n", school)
		for _, n := range list {
			b.WriteString("- " + n.Title + "\n")
		}
		if base != "" {
			b.WriteString("\nSee them all: " + base + "/notifications\n")
		}
		subject := plural(len(list), "notification") + " from " + school
		s.wg.Add(1)
		go func(to string, body string) {
			defer s.wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := s.sendMail(ctx, []string{to}, subject, body+emailFooter(school, true)); err != nil {
				slog.Warn("emailing daily summary", "err", err)
			}
		}(u.Email, b.String())
	}
}

// ---------------------------------------------------------------- pages

type noticeItem struct {
	*store.Notification
	When string
}

type noticesData struct {
	Items  []noticeItem
	Unread int
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	list, err := s.store.ListNotifications(u.ID, 100)
	if err != nil {
		s.serverError(w, r, "listing notifications", err)
		return
	}
	d := noticesData{}
	for _, n := range list {
		if n.Unread() {
			d.Unread++
		}
		d.Items = append(d.Items, noticeItem{Notification: n, When: ago(time.Unix(n.UpdatedAt, 0), time.Now())})
	}
	s.render(w, r, http.StatusOK, "notifications", "Notifications", "notifications", d)
}

// handleNotification opens what a notification is about, marking it read.
func (s *Server) handleNotification(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	n, err := s.store.GetNotification(u.ID, id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := s.store.MarkNotificationRead(u.ID, n.ID); err != nil {
		s.logError(r, "marking notification read", err)
	}
	link := n.Link
	if !strings.HasPrefix(link, "/") || strings.HasPrefix(link, "//") {
		link = "/notifications"
	}
	http.Redirect(w, r, link, http.StatusSeeOther)
}

func (s *Server) handleNotificationsRead(w http.ResponseWriter, r *http.Request) {
	if err := s.store.MarkAllNotificationsRead(current(r).user.ID); err != nil {
		s.serverError(w, r, "marking notifications read", err)
		return
	}
	s.redirect(w, r, "/notifications", "All caught up.")
}

// handleNotifyPrefs saves what the signed-in person wants to be told about.
func (s *Server) handleNotifyPrefs(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	p := &store.NotifyPrefs{UserID: u.ID, Email: r.PostFormValue("email"), Off: map[string]bool{}}
	for _, k := range store.NotifyKinds {
		if r.PostFormValue("kind_"+k) != "1" {
			p.Off[k] = true
		}
	}
	if p.Email != store.EmailOff && u.Email == "" {
		s.setFlash(w, r, "error", "Add your email address under Profile first, then choose how to get email.")
		http.Redirect(w, r, "/account#notifications", http.StatusSeeOther)
		return
	}
	if err := s.store.SetNotifyPrefs(p); err != nil {
		s.serverError(w, r, "saving notification choices", err)
		return
	}
	msg := "Saved. You'll see notifications in Taper."
	switch p.Email {
	case store.EmailEach:
		msg = "Saved. You'll get an email for each notification, at " + u.Email + "."
	case store.EmailDaily:
		msg = "Saved. You'll get a summary each evening at " + u.Email + ", when there's something new."
	}
	s.redirect(w, r, "/account#notifications", msg)
}

// noticeKind is a kind of notification, described for the settings.
type noticeKind struct {
	Key, Label, Help string
}

// noticeKinds describes the kinds of notification for u's role.
func noticeKinds(u *store.User) []noticeKind {
	work := "Feedback on your work, and when it's marked complete or returned."
	switch {
	case u.IsMentor():
		work = "Work turned in for you to review, and feedback on your scholars' work if you're a parent."
	case u.Role == store.RoleParent:
		work = "Feedback on your scholars' work, and when it's marked complete or returned."
	}
	return []noticeKind{
		{store.NotifyAssignment, "New assignments", "When an assignment is published in your classes, or your scholars'."},
		{store.NotifyWork, "Work and feedback", work},
		{store.NotifyDue, "Due tomorrow", "The afternoon before work is due, if it isn't turned in yet."},
		{store.NotifyChat, "Chat messages", "New messages in Community and your chats. Several are combined until you read them."},
		{store.NotifyCalendar, "Calendar", "New, changed and cancelled events on the school calendar and your classes'."},
	}
}
