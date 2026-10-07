package server

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/cal"
	"github.com/bradyloveland/taper/internal/store"
)

// Each person has a private link for each calendar they can subscribe to:
// "mine" (the school calendar and their classes), "school", and "class:ID"
// for each of their classes. Each link has its own token, so it can be reset
// without touching the others.

// linkToken returns u's token for a calendar, making one if needed.
func (s *Server) linkToken(u *store.User, scope string) (string, error) {
	sealed, err := s.store.CalendarLink(u.ID, scope)
	if err != nil {
		return "", err
	}
	if sealed != "" {
		if tok, err := s.box.Open(sealed); err == nil {
			return tok, nil
		}
	}
	return s.newLinkToken(u, scope)
}

func (s *Server) newLinkToken(u *store.User, scope string) (string, error) {
	tok := auth.Token(24)
	if err := s.store.SetCalendarLink(u.ID, scope, auth.HashToken(tok), s.box.Seal(tok)); err != nil {
		return "", err
	}
	return tok, nil
}

type feedLink struct {
	Scope  string
	Label  string
	Color  string
	Anchor string       // id on the page, so a class page can link to its own
	HTTPS  string       // https://… for Google and Outlook
	Webcal template.URL // webcal://… for Apple Calendar (built here, so safe)
	Google string       // a link that adds it to Google Calendar
}

type subscribeData struct {
	Links []feedLink
	HTTPS bool // the address is https, which Google and Outlook need
}

// scopes lists the calendars u can subscribe to, with labels and colors.
func (s *Server) scopes(u *store.User) ([]feedLink, error) {
	out := []feedLink{
		{Scope: "mine", Label: "My calendar: the school calendar and all my classes", Anchor: "mine"},
		{Scope: "school", Label: "School calendar only", Anchor: "school"},
	}
	classes, err := s.store.ListClassesFor(u.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range classes {
		id := strconv.FormatInt(c.ID, 10)
		out = append(out, feedLink{Scope: "class:" + id, Label: c.Name, Color: c.Color, Anchor: "class-" + id})
	}
	return out, nil
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	links, err := s.scopes(u)
	if err != nil {
		s.serverError(w, r, "listing calendars", err)
		return
	}
	base := s.baseURL(r)
	for i := range links {
		tok, err := s.linkToken(u, links[i].Scope)
		if err != nil {
			s.serverError(w, r, "making calendar link", err)
			return
		}
		l := &links[i]
		l.HTTPS = base + "/ical/" + tok + ".ics"
		webcal := "webcal://" + strings.TrimPrefix(strings.TrimPrefix(l.HTTPS, "https://"), "http://")
		l.Webcal = template.URL(webcal)
		l.Google = "https://calendar.google.com/calendar/r?cid=" + url.QueryEscape(webcal)
	}
	s.render(w, r, http.StatusOK, "subscribe", "Subscribe to your calendar", "calendar",
		subscribeData{Links: links, HTTPS: strings.HasPrefix(base, "https://")})
}

// handleSubscribeReset makes a new link for one calendar ("scope"), or for
// all of them ("all").
func (s *Server) handleSubscribeReset(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	scope := r.PostFormValue("scope")
	if scope == "all" {
		if err := s.store.DeleteCalendarLinks(u.ID); err != nil {
			s.serverError(w, r, "resetting calendar links", err)
			return
		}
		slog.Info("all calendar links reset", "username", u.Username)
		s.redirect(w, r, "/calendar/subscribe", "All your calendar links are new. The old ones stopped working; subscribe again with these.")
		return
	}
	links, err := s.scopes(u)
	if err != nil {
		s.serverError(w, r, "listing calendars", err)
		return
	}
	for _, l := range links {
		if l.Scope != scope {
			continue
		}
		if _, err := s.newLinkToken(u, scope); err != nil {
			s.serverError(w, r, "resetting calendar link", err)
			return
		}
		slog.Info("calendar link reset", "username", u.Username, "calendar", scope)
		name := strings.SplitN(l.Label, ":", 2)[0]
		s.redirect(w, r, "/calendar/subscribe#"+l.Anchor, "The link for "+name+" is new. The old one stopped working; subscribe again with this one. Your other links are unchanged.")
		return
	}
	s.notFound(w, r)
}

// feedWindow is how far back and ahead feeds reach.
const (
	feedBack  = 365
	feedAhead = 730
)

// handleFeed serves /ical/{token}.ics. The token is the only sign-in, so
// anything wrong is a plain 404.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	tok, ok := strings.CutSuffix(r.PathValue("file"), ".ics")
	if !ok || tok == "" {
		http.NotFound(w, r)
		return
	}
	u, scope, err := s.store.CalendarLinkByToken(auth.HashToken(tok))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.logError(r, "looking up calendar link", err)
		}
		http.NotFound(w, r)
		return
	}
	q, label, class, msg := s.calendarFor(u, scope)
	if msg != "" {
		http.NotFound(w, r)
		return
	}
	// A class link only works while its owner is in the class.
	if class != nil {
		if role, _ := s.store.ClassRole(class.ID, u.ID); role == "" {
			http.NotFound(w, r)
			return
		}
	}
	today := s.today()
	from, to := today.AddDate(0, 0, -feedBack), today.AddDate(0, 0, feedAhead)
	q.From, q.To = cal.FormatDate(from), cal.FormatDate(to)
	events, err := s.store.ListEvents(q)
	if err != nil {
		s.logError(r, "listing events for a feed", err)
		http.Error(w, "Try again later.", http.StatusInternalServerError)
		return
	}
	school, _ := s.appNames()
	base := s.baseURL(r)
	var items []cal.FeedItem
	for _, o := range s.expand(events, from, to) {
		e := o.Event
		it := cal.FeedItem{UID: e.UID, Sequence: e.Sequence, AllDay: o.AllDay, Summary: e.Title, Location: e.Location,
			Description: e.Description, Updated: time.Unix(e.UpdatedAt, 0), URL: base + o.Link()}
		if o.Repeats {
			it.UID = cal.FormatDate(o.Date) + "-" + e.UID
		}
		if o.AllDay {
			days := o.days()
			it.Start, it.End = days[0], days[len(days)-1].AddDate(0, 0, 1)
		} else {
			it.Start, it.End = o.Start, o.End
		}
		if e.ClassID != 0 {
			it.Categories = e.ClassName
			if class == nil {
				it.Summary = e.Title + " (" + e.ClassName + ")"
			}
		} else if e.Closed {
			it.Categories = "No school"
		}
		items = append(items, it)
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s.ics"`, strings.ReplaceAll(scope, ":", "-")))
	w.Header().Set("Cache-Control", "private, max-age=900")
	if err := cal.WriteICS(w, school+": "+label, "Taper calendar for "+u.DisplayName, items); err != nil {
		s.logError(r, "writing feed", err)
	}
}
