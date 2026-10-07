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

// feedToken returns u's subscription token, making one if needed.
func (s *Server) feedToken(u *store.User) (string, error) {
	sealed, err := s.store.CalendarFeed(u.ID)
	if err != nil {
		return "", err
	}
	if sealed != "" {
		if tok, err := s.box.Open(sealed); err == nil {
			return tok, nil
		}
	}
	return s.newFeedToken(u)
}

func (s *Server) newFeedToken(u *store.User) (string, error) {
	tok := auth.Token(24)
	if err := s.store.SetCalendarFeed(u.ID, auth.HashToken(tok), s.box.Seal(tok)); err != nil {
		return "", err
	}
	return tok, nil
}

type feedLink struct {
	Label  string
	Color  string
	HTTPS  string       // https://… for Google and Outlook
	Webcal template.URL // webcal://… for Apple Calendar (built here, so safe)
	Google string       // a link that adds it to Google Calendar
}

type subscribeData struct {
	Links []feedLink
	HTTPS bool // the address is https, which Google and Outlook need
}

func (s *Server) feedLinks(r *http.Request, tok string, u *store.User) []feedLink {
	base := s.baseURL(r)
	mk := func(label, color, file string) feedLink {
		u := base + "/ical/" + tok + "/" + file
		webcal := "webcal://" + strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		return feedLink{Label: label, Color: color, HTTPS: u, Webcal: template.URL(webcal),
			Google: "https://calendar.google.com/calendar/r?cid=" + url.QueryEscape(webcal)}
	}
	links := []feedLink{mk("My calendar: the school calendar and all my classes", "", "mine.ics"), mk("School calendar only", "", "school.ics")}
	if classes, err := s.store.ListClassesFor(u.ID); err == nil {
		for _, c := range classes {
			links = append(links, mk(c.Name, c.Color, "class-"+strconv.FormatInt(c.ID, 10)+".ics"))
		}
	}
	return links
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	tok, err := s.feedToken(u)
	if err != nil {
		s.serverError(w, r, "making calendar link", err)
		return
	}
	s.render(w, r, http.StatusOK, "subscribe", "Subscribe to your calendar", "calendar",
		subscribeData{Links: s.feedLinks(r, tok, u), HTTPS: strings.HasPrefix(s.baseURL(r), "https://")})
}

func (s *Server) handleSubscribeReset(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	if _, err := s.newFeedToken(u); err != nil {
		s.serverError(w, r, "resetting calendar link", err)
		return
	}
	slog.Info("calendar links reset", "username", u.Username)
	s.redirect(w, r, "/calendar/subscribe", "Your calendar links are new. The old ones stopped working; subscribe again with these.")
}

// feedWindow is how far back and ahead feeds reach.
const (
	feedBack  = 365
	feedAhead = 730
)

// handleFeed serves /ical/{token}/{file}: mine.ics, school.ics or class-ID.ics.
// The token is the only sign-in, so anything wrong is a plain 404.
func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	tok, file := r.PathValue("token"), r.PathValue("file")
	u, err := s.store.UserByCalendarFeed(auth.HashToken(tok))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.logError(r, "looking up calendar link", err)
		}
		http.NotFound(w, r)
		return
	}
	key := strings.TrimSuffix(file, ".ics")
	if key == file {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(key, "class-") {
		key = "class:" + strings.TrimPrefix(key, "class-")
	}
	q, label, class, msg := s.calendarFor(u, key)
	if msg != "" {
		http.NotFound(w, r)
		return
	}
	// A class calendar link only works for its members, even for mentors
	// and admins who could open the class's page: it lists their classes.
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
			Updated: time.Unix(e.UpdatedAt, 0), URL: base + o.Link()}
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
		it.Description = e.Description
		items = append(items, it)
	}
	name := school + ": " + label
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, file))
	w.Header().Set("Cache-Control", "private, max-age=900")
	if err := cal.WriteICS(w, name, "Taper calendar for "+u.DisplayName, items); err != nil {
		s.logError(r, "writing feed", err)
	}
}
