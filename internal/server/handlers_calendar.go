package server

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bradyloveland/taper/internal/cal"
	"github.com/bradyloveland/taper/internal/markdown"
	"github.com/bradyloveland/taper/internal/store"
)

// settingTimeZone is the school's IANA time zone, like "America/Denver".
// Empty means the server's own.
const settingTimeZone = "time_zone"

// loc returns the school's time zone.
func (s *Server) loc() *time.Location {
	var name string
	_, _ = s.store.GetSetting(settingTimeZone, &name)
	if name != "" {
		if l, err := time.LoadLocation(name); err == nil {
			return l
		}
	}
	return time.Local
}

// today is the current date at the school.
func (s *Server) today() time.Time { return cal.Today(s.loc(), time.Now()) }

// occurrence is one time an event happens.
type occurrence struct {
	Event      *store.CalEvent
	Date       time.Time // the day it starts
	Start, End time.Time // in the school's time zone; for all-day, midnight to midnight
	AllDay     bool
	Repeats    bool
}

// When describes an occurrence's time, like "10:00 AM–11:30 AM" or "All day".
func (o occurrence) When() string {
	if o.AllDay {
		if days := o.days(); len(days) > 1 {
			return days[0].Format("Jan 2") + " – " + days[len(days)-1].Format("Jan 2")
		}
		return "All day"
	}
	if o.Start.YearDay() != o.End.YearDay() || o.Start.Year() != o.End.Year() {
		return o.Start.Format("Jan 2, 3:04 PM") + " – " + o.End.Format("Jan 2, 3:04 PM")
	}
	return o.Start.Format("3:04 PM") + " – " + o.End.Format("3:04 PM")
}

// StartClock is the start time alone, or "" for all day.
func (o occurrence) StartClock() string {
	if o.AllDay {
		return ""
	}
	return o.Start.Format("3:04 PM")
}

// Link is the occurrence's page.
func (o occurrence) Link() string {
	return fmt.Sprintf("/events/%d?date=%s", o.Event.ID, cal.FormatDate(o.Date))
}

// expand turns events into their occurrences between from and to, sorted.
func (s *Server) expand(events []*store.CalEvent, from, to time.Time) []occurrence {
	loc := s.loc()
	var out []occurrence
	for _, e := range events {
		r, err := e.Rule()
		if err != nil {
			slog.Warn("skipping a damaged event", "event", e.ID, "err", err)
			continue
		}
		for _, d := range r.Occurrences(from, to) {
			start, end := r.Bounds(d, loc)
			out = append(out, occurrence{Event: e, Date: d, Start: start, End: end, AllDay: r.AllDay(), Repeats: e.Repeat != ""})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Date.Equal(out[j].Date) {
			return out[i].Date.Before(out[j].Date)
		}
		if out[i].AllDay != out[j].AllDay {
			return out[i].AllDay // all-day first
		}
		return out[i].Start.Before(out[j].Start)
	})
	return out
}

// calChoice is a calendar someone can look at.
type calChoice struct {
	Key   string // "mine", "school" or "class:12"
	Label string
	Color string
}

// calendarFor works out which calendars a choice covers for u, and its label.
// It returns an error message if u may not see it.
func (s *Server) calendarFor(u *store.User, key string) (store.EventQuery, string, *store.Class, string) {
	switch {
	case key == "" || key == "mine":
		classes, err := s.store.ListClassesFor(u.ID)
		if err != nil {
			return store.EventQuery{}, "", nil, "couldn't list your classes"
		}
		q := store.EventQuery{School: true}
		for _, c := range classes {
			q.ClassIDs = append(q.ClassIDs, c.ID)
		}
		return q, "My calendar", nil, ""
	case key == "school":
		return store.EventQuery{School: true}, "School calendar", nil, ""
	case strings.HasPrefix(key, "class:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(key, "class:"), 10, 64)
		if err != nil {
			return store.EventQuery{}, "", nil, "no such calendar"
		}
		c, err := s.store.GetClass(id)
		if err != nil {
			return store.EventQuery{}, "", nil, "no such calendar"
		}
		a, err := s.access(u, c)
		if err != nil || !a.CanSee {
			return store.EventQuery{}, "", nil, "no such calendar"
		}
		return store.EventQuery{ClassIDs: []int64{c.ID}}, c.Name, c, ""
	}
	return store.EventQuery{}, "", nil, "no such calendar"
}

// calendarChoices lists the calendars u can pick from.
func (s *Server) calendarChoices(u *store.User) []calChoice {
	out := []calChoice{{Key: "mine", Label: "My calendar"}, {Key: "school", Label: "School calendar"}}
	if classes, err := s.store.ListClassesFor(u.ID); err == nil {
		for _, c := range classes {
			out = append(out, calChoice{Key: "class:" + strconv.FormatInt(c.ID, 10), Label: c.Name, Color: c.Color})
		}
	}
	return out
}

// ---------------------------------------------------------------- views

type calDay struct {
	Date    time.Time
	InMonth bool
	Today   bool
	Closed  bool // a no-school day
	Items   []occurrence
}

type calendarData struct {
	View      string // month, week or list
	Key       string
	Label     string
	Class     *store.Class
	Choices   []calChoice
	Date      time.Time // the date the view is around
	Title     string
	Prev      string
	Next      string
	Weeks     [][]calDay // month view
	Days      []calDay   // week and list views
	Agenda    []calDay   // month view: days with events, for phones
	CanAdd    bool
	AddTarget string // the calendar new events go to
}

func dateParam(r *http.Request, name string, def time.Time) time.Time {
	if t, err := cal.ParseDate(r.URL.Query().Get(name)); err == nil {
		return t
	}
	return def
}

func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	q := r.URL.Query()
	d := calendarData{View: q.Get("view"), Key: q.Get("cal")}
	if d.View != "week" && d.View != "list" {
		d.View = "month"
	}
	if d.Key == "" {
		d.Key = "mine"
	}
	query, label, class, msg := s.calendarFor(u, d.Key)
	if msg != "" {
		s.notFound(w, r)
		return
	}
	d.Label, d.Class, d.Choices = label, class, s.calendarChoices(u)
	if class != nil {
		found := false
		for _, c := range d.Choices {
			found = found || c.Key == d.Key
		}
		if !found {
			d.Choices = append(d.Choices, calChoice{Key: d.Key, Label: class.Name, Color: class.Color})
		}
	}
	today := s.today()
	d.Date = dateParam(r, "date", today)

	var from, to time.Time
	switch d.View {
	case "month":
		first := time.Date(d.Date.Year(), d.Date.Month(), 1, 0, 0, 0, 0, time.UTC)
		from = first.AddDate(0, 0, -int(first.Weekday()))
		last := first.AddDate(0, 1, -1)
		to = last.AddDate(0, 0, 6-int(last.Weekday()))
		d.Title = first.Format("January 2006")
		d.Prev, d.Next = cal.FormatDate(first.AddDate(0, -1, 0)), cal.FormatDate(first.AddDate(0, 1, 0))
	case "week":
		from = d.Date.AddDate(0, 0, -int(d.Date.Weekday()))
		to = from.AddDate(0, 0, 6)
		d.Title = "Week of " + from.Format("January 2, 2006")
		d.Prev, d.Next = cal.FormatDate(from.AddDate(0, 0, -7)), cal.FormatDate(from.AddDate(0, 0, 7))
	case "list":
		from = d.Date
		to = from.AddDate(0, 0, 59)
		d.Title = "From " + from.Format("January 2, 2006")
		d.Prev, d.Next = cal.FormatDate(from.AddDate(0, 0, -60)), cal.FormatDate(from.AddDate(0, 0, 60))
	}
	query.From, query.To = cal.FormatDate(from), cal.FormatDate(to)
	events, err := s.store.ListEvents(query)
	if err != nil {
		s.serverError(w, r, "listing events", err)
		return
	}
	occ := s.expand(events, from, to)
	byDay := map[string][]occurrence{}
	closed := map[string]bool{}
	for _, o := range occ {
		for _, t := range o.days() {
			day := cal.FormatDate(t)
			byDay[day] = append(byDay[day], o)
			if o.Event.Closed {
				closed[day] = true
			}
		}
	}
	mkDay := func(t time.Time) calDay {
		key := cal.FormatDate(t)
		return calDay{Date: t, InMonth: t.Month() == d.Date.Month(), Today: t.Equal(today), Closed: closed[key], Items: byDay[key]}
	}
	switch d.View {
	case "month":
		for t := from; !t.After(to); t = t.AddDate(0, 0, 7) {
			var week []calDay
			for i := 0; i < 7; i++ {
				day := mkDay(t.AddDate(0, 0, i))
				week = append(week, day)
				if day.InMonth && len(day.Items) > 0 {
					d.Agenda = append(d.Agenda, day)
				}
			}
			d.Weeks = append(d.Weeks, week)
		}
	case "week":
		for t := from; !t.After(to); t = t.AddDate(0, 0, 1) {
			d.Days = append(d.Days, mkDay(t))
		}
	case "list":
		for t := from; !t.After(to); t = t.AddDate(0, 0, 1) {
			if day := mkDay(t); len(day.Items) > 0 {
				d.Days = append(d.Days, day)
			}
		}
	}
	d.CanAdd, d.AddTarget = s.canAddTo(u, d.Key, class)
	s.render(w, r, http.StatusOK, "calendar", d.Label, "calendar", d)
}

// days lists the dates an occurrence covers.
func (o occurrence) days() []time.Time {
	last := time.Date(o.End.Year(), o.End.Month(), o.End.Day(), 0, 0, 0, 0, time.UTC)
	if o.AllDay || (o.End.Hour() == 0 && o.End.Minute() == 0 && last.After(o.Date)) {
		last = last.AddDate(0, 0, -1) // ends at midnight: the last day is the one before
	}
	var out []time.Time
	for t := o.Date; !t.After(last) && len(out) < 400; t = t.AddDate(0, 0, 1) {
		out = append(out, t)
	}
	if len(out) == 0 {
		out = append(out, o.Date)
	}
	return out
}

// canAddTo says whether u can add events from this view, and to which
// calendar ("school" or a class ID).
func (s *Server) canAddTo(u *store.User, key string, class *store.Class) (bool, string) {
	if class != nil {
		a, err := s.access(u, class)
		return err == nil && a.CanEdit, strconv.FormatInt(class.ID, 10)
	}
	if key == "school" {
		return u.IsAdmin(), "school"
	}
	// My calendar: anyone who can add to at least one calendar.
	return len(s.editableCalendars(u)) > 0, ""
}

// editableCalendars lists the calendars u may add events to.
func (s *Server) editableCalendars(u *store.User) []calChoice {
	var out []calChoice
	if u.IsAdmin() {
		out = append(out, calChoice{Key: "school", Label: "School calendar"})
	}
	var classes []*store.Class
	if u.IsAdmin() {
		classes, _ = s.store.ListClasses(store.ClassFilter{})
	} else {
		mine, _ := s.store.ListClassesFor(u.ID)
		for _, c := range mine {
			if c.MyRole == store.ClassMentor {
				classes = append(classes, c)
			}
		}
	}
	for _, c := range classes {
		out = append(out, calChoice{Key: strconv.FormatInt(c.ID, 10), Label: c.Name, Color: c.Color})
	}
	return out
}

// upcoming returns occurrences in the next days on a calendar, up to limit.
func (s *Server) upcoming(q store.EventQuery, days, limit int) []occurrence {
	from := s.today()
	to := from.AddDate(0, 0, days)
	q.From, q.To = cal.FormatDate(from), cal.FormatDate(to)
	events, err := s.store.ListEvents(q)
	if err != nil {
		slog.Error("listing upcoming events", "err", err)
		return nil
	}
	occ := s.expand(events, from, to)
	now := time.Now()
	var out []occurrence
	for _, o := range occ {
		if o.End.Before(now) {
			continue // already over today
		}
		out = append(out, o)
		if len(out) == limit {
			break
		}
	}
	return out
}

// --------------------------------------------------------------- events

// event loads the event in the URL if the signed-in person may see it.
// It also says whether they may change it.
func (s *Server) event(w http.ResponseWriter, r *http.Request) (*store.CalEvent, bool, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil, false, false
	}
	e, err := s.store.GetEvent(id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil, false, false
	}
	if err != nil {
		s.serverError(w, r, "loading event", err)
		return nil, false, false
	}
	u := current(r).user
	if e.ClassID == 0 {
		return e, u.IsAdmin(), true
	}
	c, err := s.store.GetClass(e.ClassID)
	if err != nil {
		s.notFound(w, r)
		return nil, false, false
	}
	a, err := s.access(u, c)
	if err != nil || !a.CanSee {
		s.notFound(w, r)
		return nil, false, false
	}
	return e, a.CanEdit, true
}

type eventData struct {
	Event       *store.CalEvent
	Occ         *occurrence
	Description template.HTML
	Repeats     string
	CanEdit     bool
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	e, canEdit, ok := s.event(w, r)
	if !ok {
		return
	}
	d := eventData{Event: e, CanEdit: canEdit}
	rule, err := e.Rule()
	if err != nil {
		s.serverError(w, r, "reading event", err)
		return
	}
	d.Repeats = rule.Describe()
	date := dateParam(r, "date", rule.StartDate)
	if occ := s.expand([]*store.CalEvent{e}, date, date); len(occ) > 0 {
		for i := range occ {
			if occ[i].Date.Equal(date) {
				d.Occ = &occ[i]
			}
		}
		if d.Occ == nil {
			d.Occ = &occ[0]
		}
	}
	if d.Occ == nil { // a skipped or out-of-range date: show the first
		if occ := s.expand([]*store.CalEvent{e}, rule.StartDate, rule.StartDate); len(occ) > 0 {
			d.Occ = &occ[0]
		}
	}
	if e.Description != "" {
		d.Description, _ = markdown.Render([]byte(e.Description), markdown.Options{})
	}
	s.render(w, r, http.StatusOK, "event", e.Title, "calendar", d)
}

type eventFormData struct {
	Field     string // the field the error is about
	IsNew     bool
	Event     *store.CalEvent
	Form      map[string]string
	Days      map[string]bool
	Calendars []calChoice
	Weekdays  []time.Weekday
	Error     string
}

var allWeekdays = []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday}

func eventForm(e *store.CalEvent) (map[string]string, map[string]bool) {
	f := map[string]string{"title": e.Title, "description": e.Description, "location": e.Location,
		"start_date": e.StartDate, "start_time": e.StartTime, "end_date": e.EndDate, "end_time": e.EndTime,
		"repeat": e.Repeat, "until": e.RepeatUntil, "calendar": "school"}
	if e.ClassID != 0 {
		f["calendar"] = strconv.FormatInt(e.ClassID, 10)
	}
	if e.AllDay() {
		f["all_day"] = "1"
	}
	if e.Closed {
		f["closed"] = "1"
	}
	days := map[string]bool{}
	for _, d := range cal.ParseDays(e.RepeatDays) {
		days[strconv.Itoa(int(d))] = true
	}
	return f, days
}

// readEvent validates the event form into e. It returns the form values to
// show again and a message if something's wrong.
func readEvent(r *http.Request, e *store.CalEvent) (map[string]string, map[string]bool, string, string) {
	f := formValues(r, "title", "location", "start_date", "start_time", "end_date", "end_time", "repeat", "until", "calendar", "all_day", "closed")
	f["description"] = strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("description"), "\r\n", "\n"))
	_ = r.ParseForm()
	days := map[string]bool{}
	var weekdays []time.Weekday
	for _, v := range r.PostForm["day"] {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 6 && !days[v] {
			days[v] = true
			weekdays = append(weekdays, time.Weekday(n))
		}
	}
	title, msg := cleanName(f["title"], "title")
	if msg != "" {
		return f, days, msg, "title"
	}
	if utf8.RuneCountInString(f["location"]) > 200 || utf8.RuneCountInString(f["description"]) > 20000 {
		return f, days, "The place or details are too long.", ""
	}
	start, err := cal.ParseDate(f["start_date"])
	if err != nil {
		return f, days, "Choose the day it starts.", "start_date"
	}
	end := start
	if f["end_date"] != "" {
		if end, err = cal.ParseDate(f["end_date"]); err != nil {
			return f, days, "The end date doesn't look right.", "end_date"
		}
	}
	if end.Before(start) {
		return f, days, "The end date is before the start date. Choose a later end date.", "end_date"
	}
	if end.Sub(start) > 366*24*time.Hour {
		return f, days, "An event can last a year at most.", "end_date"
	}
	allDay := f["all_day"] == "1"
	startTime, endTime := "", ""
	if !allDay {
		sm, err1 := cal.ParseClock(f["start_time"])
		em, err2 := cal.ParseClock(f["end_time"])
		if err1 != nil || err2 != nil {
			return f, days, "Enter a start and end time, or tick All day.", "start_time"
		}
		if end.Equal(start) && em <= sm {
			return f, days, fmt.Sprintf("It ends (%s) before it starts (%s). Choose a later end time, or a later end date if it goes past midnight.",
				clock12(em), clock12(sm)), "end_time"
		}
		startTime, endTime = fmt.Sprintf("%02d:%02d", sm/60, sm%60), fmt.Sprintf("%02d:%02d", em/60, em%60)
	}
	repeat := f["repeat"]
	switch repeat {
	case cal.None, cal.Daily, cal.Weekly, cal.Monthly, cal.Yearly:
	default:
		repeat = cal.None
	}
	until := ""
	if repeat != cal.None {
		if !end.Equal(start) && (allDay || !end.Equal(start.AddDate(0, 0, 1))) {
			return f, days, "Repeating events must start and end on the same day (or end overnight). Change the end date.", "end_date"
		}
		if f["until"] != "" {
			u, err := cal.ParseDate(f["until"])
			if err != nil || u.Before(start) {
				return f, days, "The repeat should end on or after the first day.", "until"
			}
			until = cal.FormatDate(u)
		}
		if repeat == cal.Weekly && len(weekdays) == 0 {
			weekdays = []time.Weekday{start.Weekday()}
		}
	}
	sort.Slice(weekdays, func(i, j int) bool { return weekdays[i] < weekdays[j] })
	e.Title, e.Location, e.Description = title, strings.Join(strings.Fields(f["location"]), " "), f["description"]
	e.StartDate, e.EndDate, e.StartTime, e.EndTime = cal.FormatDate(start), cal.FormatDate(end), startTime, endTime
	e.Repeat, e.RepeatUntil, e.RepeatDays = repeat, until, ""
	if repeat == cal.Weekly {
		e.RepeatDays = cal.FormatDays(weekdays)
	}
	e.Closed = f["closed"] == "1" && e.ClassID == 0
	return f, days, "", ""
}

func (s *Server) renderEventForm(w http.ResponseWriter, r *http.Request, status int, d eventFormData) {
	d.Calendars = s.editableCalendars(current(r).user)
	d.Weekdays = allWeekdays
	title := "New event"
	if !d.IsNew {
		title = "Edit " + d.Event.Title
	}
	s.render(w, r, status, "event-form", title, "calendar", d)
}

func (s *Server) handleEventNewForm(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	cals := s.editableCalendars(u)
	if len(cals) == 0 {
		s.renderError(w, r, http.StatusForbidden, "You can't add events", "Admins add events to the school calendar, and mentors to their classes' calendars.")
		return
	}
	date := cal.FormatDate(dateParam(r, "date", s.today()))
	target := r.URL.Query().Get("cal")
	ok := false
	for _, c := range cals {
		ok = ok || c.Key == target
	}
	if !ok {
		target = cals[0].Key
	}
	f := map[string]string{"start_date": date, "end_date": date, "start_time": "09:00", "end_time": "10:00", "calendar": target}
	s.renderEventForm(w, r, http.StatusOK, eventFormData{IsNew: true, Form: f, Days: map[string]bool{}})
}

// targetCalendar checks the calendar chosen in the form, returning the class
// ID (0 for school) or false if u may not add to it.
func (s *Server) targetCalendar(u *store.User, key string) (int64, bool) {
	for _, c := range s.editableCalendars(u) {
		if c.Key == key {
			if key == "school" {
				return 0, true
			}
			id, err := strconv.ParseInt(key, 10, 64)
			return id, err == nil
		}
	}
	return 0, false
}

func (s *Server) handleEventCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	classID, ok := s.targetCalendar(u, r.PostFormValue("calendar"))
	if !ok {
		s.renderError(w, r, http.StatusForbidden, "You can't add events there", "Admins add events to the school calendar, and mentors to their classes' calendars.")
		return
	}
	e := &store.CalEvent{ClassID: classID, CreatedBy: u.ID}
	f, days, msg, field := readEvent(r, e)
	if msg != "" {
		s.renderEventForm(w, r, http.StatusUnprocessableEntity, eventFormData{IsNew: true, Form: f, Days: days, Error: msg, Field: field})
		return
	}
	if err := s.store.CreateEvent(e); err != nil {
		s.serverError(w, r, "creating event", err)
		return
	}
	slog.Info("event created", "by", u.Username, "event", e.ID, "class", classID)
	s.redirect(w, r, fmt.Sprintf("/events/%d?date=%s", e.ID, e.StartDate), e.Title+" is on the calendar.")
}

func (s *Server) handleEventEditForm(w http.ResponseWriter, r *http.Request) {
	e, canEdit, ok := s.event(w, r)
	if !ok {
		return
	}
	if !canEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't change this event", "Admins change school events, and mentors their classes' events.")
		return
	}
	f, days := eventForm(e)
	s.renderEventForm(w, r, http.StatusOK, eventFormData{Event: e, Form: f, Days: days})
}

func (s *Server) handleEventUpdate(w http.ResponseWriter, r *http.Request) {
	e, canEdit, ok := s.event(w, r)
	if !ok {
		return
	}
	if !canEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't change this event", "Admins change school events, and mentors their classes' events.")
		return
	}
	updated := *e
	f, days, msg, field := readEvent(r, &updated)
	if msg != "" {
		s.renderEventForm(w, r, http.StatusUnprocessableEntity, eventFormData{Event: e, Form: f, Days: days, Error: msg, Field: field})
		return
	}
	if err := s.store.UpdateEvent(&updated); err != nil {
		s.serverError(w, r, "saving event", err)
		return
	}
	s.redirect(w, r, fmt.Sprintf("/events/%d?date=%s", e.ID, updated.StartDate), "Saved.")
}

func (s *Server) handleEventDelete(w http.ResponseWriter, r *http.Request) {
	e, canEdit, ok := s.event(w, r)
	if !ok {
		return
	}
	if !canEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't change this event", "Admins change school events, and mentors their classes' events.")
		return
	}
	back := "/calendar"
	if e.ClassID != 0 {
		back += "?cal=class:" + strconv.FormatInt(e.ClassID, 10)
	} else {
		back += "?cal=school"
	}
	if date := r.PostFormValue("date"); date != "" && e.Repeat != "" {
		if _, err := cal.ParseDate(date); err != nil {
			s.notFound(w, r)
			return
		}
		if err := s.store.SkipDate(e.ID, date); err != nil {
			s.serverError(w, r, "removing a date", err)
			return
		}
		t, _ := cal.ParseDate(date)
		s.redirect(w, r, back, e.Title+" on "+t.Format("Monday, January 2")+" is removed. The other dates stay.")
		return
	}
	if err := s.store.DeleteEvent(e.ID); err != nil {
		s.serverError(w, r, "deleting event", err)
		return
	}
	slog.Info("event deleted", "by", current(r).user.Username, "event", e.ID)
	s.redirect(w, r, back, e.Title+" is deleted.")
}

// clock12 writes minutes after midnight as "6:00 PM".
func clock12(m int) string {
	return time.Date(2000, 1, 1, m/60, m%60, 0, 0, time.UTC).Format("3:04 PM")
}
