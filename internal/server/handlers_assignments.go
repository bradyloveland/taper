package server

import (
	"encoding/json"
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

// Assignments belong to a class. Its mentors (and admins) set and review
// them; its scholars see them once published, and turn in their work.
// There are no grades: work is turned in, then marked Complete or sent back
// as Needs another look, with written feedback.

const maxWork = 100000 // characters of written work

// assignAccess is what the signed-in person may do with an assignment.
type assignAccess struct {
	Class     *store.Class
	CanSee    bool
	CanEdit   bool // change it: admins and the class's mentors, while the class is current
	CanReview bool // see everyone's work and give feedback: admins and the class's mentors
	Scholar   bool // a scholar in the class, who turns in work
}

func (s *Server) assignmentAccess(u *store.User, a *store.Assignment) (assignAccess, error) {
	c, err := s.store.GetClass(a.ClassID)
	if err != nil {
		return assignAccess{}, err
	}
	ca, err := s.access(u, c)
	if err != nil {
		return assignAccess{}, err
	}
	acc := assignAccess{Class: c, CanEdit: ca.CanEdit, Scholar: ca.Role == store.ClassScholar}
	acc.CanReview = u.IsLeader() || ca.Role == store.ClassMentor
	acc.CanSee = acc.CanReview || (ca.CanSee && a.Published(time.Now().Unix()))
	return acc, nil
}

// assignment loads the assignment in the URL if the signed-in person may
// see it, writing an error page if not.
func (s *Server) assignment(w http.ResponseWriter, r *http.Request) (*store.Assignment, assignAccess, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil, assignAccess{}, false
	}
	a, err := s.store.GetAssignment(id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil, assignAccess{}, false
	}
	if err != nil {
		s.serverError(w, r, "loading assignment", err)
		return nil, assignAccess{}, false
	}
	acc, err := s.assignmentAccess(current(r).user, a)
	if err != nil {
		s.serverError(w, r, "checking assignment access", err)
		return nil, assignAccess{}, false
	}
	if !acc.CanSee {
		s.notFound(w, r)
		return nil, assignAccess{}, false
	}
	return a, acc, true
}

func assignmentPath(id int64) string { return "/assignments/" + strconv.FormatInt(id, 10) }

// ------------------------------------------------------------ due dates

// dueAt is when an assignment is due in the school's time zone: its due
// time, or the end of its due day. It's zero if there's no due date.
func dueAt(a *store.Assignment, loc *time.Location) time.Time {
	d, err := cal.ParseDate(a.DueDate)
	if err != nil {
		return time.Time{}
	}
	m := 23*60 + 59
	if a.DueTime != "" {
		if t, err := cal.ParseClock(a.DueTime); err == nil {
			m = t
		}
	}
	return time.Date(d.Year(), d.Month(), d.Day(), m/60, m%60, 59*boolInt(a.DueTime == ""), 0, loc)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// dueText describes when an assignment is due, like "Due Tue, Oct 13 at 3:00 PM".
func dueText(a *store.Assignment, loc *time.Location, today time.Time) string {
	d, err := cal.ParseDate(a.DueDate)
	if err != nil {
		return "No due date"
	}
	day := d.Format("Mon, Jan 2")
	switch {
	case d.Equal(today):
		day = "today"
	case d.Equal(today.AddDate(0, 0, 1)):
		day = "tomorrow"
	case d.Year() != today.Year():
		day = d.Format("Mon, Jan 2, 2006")
	}
	if a.DueTime != "" {
		return "Due " + day + " at " + dueAt(a, loc).Format("3:04 PM")
	}
	return "Due " + day
}

// workState describes a scholar's work for showing.
type workState struct {
	Label string // "Turned in", "Needs another look", …
	Kind  string // for styling: todo, late, in, needs, done
	Late  bool   // turned in after the due time, or still not turned in after it
}

func stateOf(a *store.Assignment, wk *store.Submission, loc *time.Location, now time.Time) workState {
	due := dueAt(a, loc)
	status := store.WorkDraft
	if wk != nil {
		status = wk.Status
	}
	switch status {
	case store.WorkComplete:
		return workState{Label: "Complete", Kind: "done"}
	case store.WorkNeedsWork:
		return workState{Label: "Needs another look", Kind: "needs"}
	case store.WorkTurnedIn:
		late := !due.IsZero() && time.Unix(wk.TurnedInAt, 0).After(due)
		return workState{Label: "Turned in", Kind: "in", Late: late}
	}
	if !due.IsZero() && now.After(due) {
		return workState{Label: "Not turned in", Kind: "late", Late: true}
	}
	return workState{Label: "Not turned in", Kind: "todo"}
}

// assignItem is an assignment in a list.
type assignItem struct {
	*store.Assignment
	Due      string
	Overdue  bool
	State    workState // for scholars
	TurnedIn int       // for mentors: turned in, waiting for review
	Done     int       // marked complete
	Scholars int
	Status   string // for mentors: Draft, Scheduled, or ""
}

func (s *Server) assignItems(list []*store.Assignment, u *store.User, reviewer func(*store.Assignment) bool) []assignItem {
	loc, now := s.loc(), time.Now()
	today := s.today()
	var ids []int64
	for _, a := range list {
		ids = append(ids, a.ID)
	}
	mine, err := s.store.SubmissionsFor(u.ID, ids)
	if err != nil {
		slog.Error("listing work", "err", err)
	}
	counts, err := s.store.StatusCounts(ids)
	if err != nil {
		slog.Error("counting work", "err", err)
	}
	scholars := map[int64]int{}
	out := make([]assignItem, 0, len(list))
	for _, a := range list {
		it := assignItem{Assignment: a, Due: dueText(a, loc, today)}
		due := dueAt(a, loc)
		it.Overdue = !due.IsZero() && now.After(due)
		if reviewer(a) {
			it.TurnedIn = counts[a.ID][store.WorkTurnedIn]
			it.Done = counts[a.ID][store.WorkComplete]
			n, ok := scholars[a.ClassID]
			if !ok {
				if c, err := s.store.GetClass(a.ClassID); err == nil {
					n = c.Scholars
				}
				scholars[a.ClassID] = n
			}
			it.Scholars = n
			switch {
			case a.Draft():
				it.Status = "Draft"
			case !a.Published(now.Unix()):
				it.Status = "Publishes " + time.Unix(a.PublishAt, 0).In(loc).Format("Mon, Jan 2 at 3:04 PM")
			}
		} else {
			it.State = stateOf(a, mine[a.ID], loc, now)
		}
		out = append(out, it)
	}
	return out
}

// dueOccurrences returns the published assignments due between from and to
// in the classes, as calendar items.
func (s *Server) dueOccurrences(classIDs []int64, from, to time.Time) []occurrence {
	if len(classIDs) == 0 {
		return nil
	}
	list, err := s.store.ListAssignments(store.AssignmentQuery{ClassIDs: classIDs, PublishedOnly: true, Now: time.Now().Unix(),
		DueFrom: cal.FormatDate(from), DueTo: cal.FormatDate(to)})
	if err != nil {
		slog.Error("listing due dates", "err", err)
		return nil
	}
	loc := s.loc()
	var out []occurrence
	for _, a := range list {
		d, err := cal.ParseDate(a.DueDate)
		if err != nil {
			continue
		}
		e := &store.CalEvent{ID: a.ID, ClassID: a.ClassID, Title: "Due: " + a.Title, ClassName: a.ClassName,
			ClassColor: a.ClassColor, UID: fmt.Sprintf("due-%d@taper", a.ID), UpdatedAt: a.UpdatedAt, StartDate: a.DueDate}
		o := occurrence{Event: e, Date: d, URL: assignmentPath(a.ID), Due: true}
		if a.DueTime == "" {
			o.AllDay = true
			o.Start = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
			o.End = o.Start.AddDate(0, 0, 1)
		} else {
			o.Start = dueAt(a, loc)
			o.End = o.Start
		}
		out = append(out, o)
	}
	return out
}

// --------------------------------------------------------------- lists

type assignmentsData struct {
	Class    *store.Class // the class, for a class's list
	Access   classAccess
	Reviewer bool         // the viewer sets and reviews work in these classes
	Upcoming []assignItem // due from today, or no due date
	Past     []assignItem
}

// handleClassAssignments lists all of a class's assignments.
func (s *Server) handleClassAssignments(w http.ResponseWriter, r *http.Request) {
	c, ca, ok := s.class(w, r)
	if !ok {
		return
	}
	u := current(r).user
	reviewer := u.IsLeader() || ca.Role == store.ClassMentor
	list, err := s.store.ListAssignments(store.AssignmentQuery{ClassIDs: []int64{c.ID}, PublishedOnly: !reviewer, Now: time.Now().Unix()})
	if err != nil {
		s.serverError(w, r, "listing assignments", err)
		return
	}
	d := assignmentsData{Class: c, Access: ca, Reviewer: reviewer}
	d.Upcoming, d.Past = s.splitByDue(s.assignItems(list, u, func(*store.Assignment) bool { return reviewer }), ca.Role == store.ClassScholar)
	if !reviewer && ca.Role != store.ClassScholar { // a mentor looking in: no statuses
		for i := range d.Upcoming {
			d.Upcoming[i].State = workState{}
		}
		for i := range d.Past {
			d.Past[i].State = workState{}
		}
	}
	s.render(w, r, http.StatusOK, "class-assignments", "Assignments in "+c.Name, "classes", d)
}

// splitByDue splits a list into upcoming (due from today, or no due date,
// or for scholars not yet done) and past, newest first.
func (s *Server) splitByDue(items []assignItem, scholar bool) (upcoming, past []assignItem) {
	today := cal.FormatDate(s.today())
	for _, it := range items {
		open := scholar && (it.State.Kind == "todo" || it.State.Kind == "late" || it.State.Kind == "needs")
		if it.DueDate == "" || it.DueDate >= today || open || it.TurnedIn > 0 {
			upcoming = append(upcoming, it)
		} else {
			past = append(past, it)
		}
	}
	sort.SliceStable(past, func(i, j int) bool { return past[i].DueDate > past[j].DueDate })
	return
}

// classAssignments returns the items for a class page's Assignments card.
func (s *Server) classAssignments(c *store.Class, ca classAccess, u *store.User, limit int) ([]assignItem, int) {
	reviewer := u.IsLeader() || ca.Role == store.ClassMentor
	list, err := s.store.ListAssignments(store.AssignmentQuery{ClassIDs: []int64{c.ID}, PublishedOnly: !reviewer, Now: time.Now().Unix()})
	if err != nil {
		slog.Error("listing assignments", "err", err)
		return nil, 0
	}
	items := s.assignItems(list, u, func(*store.Assignment) bool { return reviewer })
	if !reviewer && ca.Role != store.ClassScholar {
		for i := range items {
			items[i].State = workState{}
		}
	}
	upcoming, _ := s.splitByDue(items, ca.Role == store.ClassScholar)
	if len(upcoming) > limit {
		upcoming = upcoming[:limit]
	}
	return upcoming, len(list)
}

// ------------------------------------------------------------ one assignment

type fileItem struct {
	*store.File
	Link string
	Size string
}

// IsImage reports whether the file can be shown as a picture.
func (f fileItem) IsImage() bool {
	return strings.HasPrefix(f.ContentType, "image/") && inlineTypes[f.ContentType]
}

func fileItems(list []*store.File) []fileItem {
	out := make([]fileItem, len(list))
	for i, f := range list {
		out[i] = fileItem{File: f, Link: fileLink(f), Size: humanSize(f.Size)}
	}
	return out
}

// rosterRow is one scholar's work, on the assignment page for mentors.
type rosterRow struct {
	Scholar *store.Member
	Work    *store.Submission
	State   workState
	When    int64 // turned in
}

type assignmentData struct {
	A            *store.Assignment
	Access       assignAccess
	Due          string
	Overdue      bool
	Status       string // Draft or Publishes …, for mentors
	Instructions template.HTML
	Files        []fileItem

	// A scholar's own work.
	Work      *store.Submission
	WorkFiles []fileItem
	State     workState
	Feedback  template.HTML
	History   []store.HistoryItem
	Editable  bool
	Body      string        // what's in the box
	BodyHTML  template.HTML // the work as written, once turned in

	// Everyone's work, for mentors.
	Roster   []rosterRow
	TurnedIn int
	Done     int
	Needs    int
}

func (s *Server) handleAssignment(w http.ResponseWriter, r *http.Request) {
	a, acc, ok := s.assignment(w, r)
	if !ok {
		return
	}
	u := current(r).user
	loc, now := s.loc(), time.Now()
	d := assignmentData{A: a, Access: acc, Due: dueText(a, loc, s.today())}
	if due := dueAt(a, loc); !due.IsZero() {
		d.Overdue = now.After(due)
	}
	switch {
	case a.Draft():
		d.Status = "Draft: scholars can't see it yet."
	case !a.Published(now.Unix()):
		d.Status = "Scholars see it from " + time.Unix(a.PublishAt, 0).In(loc).Format("Monday, January 2 at 3:04 PM") + "."
	}
	if a.Instructions != "" {
		d.Instructions, _ = markdown.Render([]byte(a.Instructions), markdown.Options{})
	}
	files, err := s.store.ListFiles(store.FileForAssignment, a.ID)
	if err != nil {
		s.serverError(w, r, "listing files", err)
		return
	}
	d.Files = fileItems(files)
	if acc.Scholar {
		wk, err := s.store.GetSubmission(a.ID, u.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.serverError(w, r, "loading work", err)
			return
		}
		if err == nil {
			d.Work = wk
			d.Body = wk.Body
			if wk.Body != "" {
				d.BodyHTML, _ = markdown.Render([]byte(wk.Body), markdown.Options{HardWraps: true})
			}
			list, _ := s.store.ListFiles(store.FileForSubmission, wk.ID)
			d.WorkFiles = fileItems(list)
			if wk.Feedback != "" {
				d.Feedback, _ = markdown.Render([]byte(wk.Feedback), markdown.Options{HardWraps: true})
			}
			d.History, _ = s.store.SubmissionHistory(wk.ID)
		}
		d.State = stateOf(a, d.Work, loc, now)
		d.Editable = !acc.Class.Archived && (d.Work == nil || d.Work.Editable())
	}
	if acc.CanReview {
		members, err := s.store.ClassMembers(a.ClassID, false)
		if err != nil {
			s.serverError(w, r, "listing members", err)
			return
		}
		work, err := s.store.ListSubmissions(a.ID)
		if err != nil {
			s.serverError(w, r, "listing work", err)
			return
		}
		for _, m := range members {
			if m.ClassRole != store.ClassScholar {
				continue
			}
			row := rosterRow{Scholar: m, Work: work[m.ID]}
			row.State = stateOf(a, row.Work, loc, now)
			if row.Work != nil {
				row.When = row.Work.TurnedInAt
			}
			switch row.State.Kind {
			case "in":
				d.TurnedIn++
			case "done":
				d.Done++
			case "needs":
				d.Needs++
			}
			d.Roster = append(d.Roster, row)
		}
		// Waiting for review first, then the rest by name (as listed).
		rank := map[string]int{"in": 0, "needs": 1, "late": 2, "todo": 2, "done": 3}
		sort.SliceStable(d.Roster, func(i, j int) bool { return rank[d.Roster[i].State.Kind] < rank[d.Roster[j].State.Kind] })
	}
	s.render(w, r, http.StatusOK, "assignment", a.Title, "classes", d)
}

// ------------------------------------------------------------ the form

type assignFormData struct {
	IsNew bool
	Class *store.Class
	A     *store.Assignment
	Form  map[string]string
	Files []fileItem
	Error string
	Field string
}

func assignForm(a *store.Assignment, loc *time.Location) map[string]string {
	f := map[string]string{"title": a.Title, "instructions": a.Instructions, "due_date": a.DueDate, "due_time": a.DueTime}
	if a.WorkOnline {
		f["work_online"] = "1"
	}
	if a.AllowFiles {
		f["allow_files"] = "1"
	}
	switch {
	case a.Draft():
		f["publish"] = "draft"
	case a.PublishAt > time.Now().Unix():
		f["publish"] = "later"
		t := time.Unix(a.PublishAt, 0).In(loc)
		f["publish_date"], f["publish_time"] = t.Format("2006-01-02"), t.Format("15:04")
	default:
		f["publish"] = "now"
	}
	return f
}

// readAssignment validates the form into a.
func (s *Server) readAssignment(r *http.Request, a *store.Assignment) (map[string]string, string, string) {
	f := formValues(r, "title", "due_date", "due_time", "publish", "publish_date", "publish_time", "work_online", "allow_files")
	f["instructions"] = strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("instructions"), "\r\n", "\n"))
	title, msg := cleanName(f["title"], "title")
	if msg != "" {
		return f, msg, "title"
	}
	if utf8.RuneCountInString(f["instructions"]) > 50000 {
		return f, "The instructions are too long. Keep them to 50,000 characters, and attach longer material as a file.", "instructions"
	}
	dueDate, dueTime := "", ""
	if f["due_date"] != "" {
		d, err := cal.ParseDate(f["due_date"])
		if err != nil {
			return f, "The due date doesn't look right.", "due_date"
		}
		dueDate = cal.FormatDate(d)
		if f["due_time"] != "" {
			m, err := cal.ParseClock(f["due_time"])
			if err != nil {
				return f, "The due time doesn't look right.", "due_time"
			}
			dueTime = fmt.Sprintf("%02d:%02d", m/60, m%60)
		}
	} else if f["due_time"] != "" {
		return f, "Choose a due date for the due time, or clear the time.", "due_date"
	}
	loc, now := s.loc(), time.Now()
	var publishAt int64
	switch f["publish"] {
	case "draft":
	case "later":
		d, err := cal.ParseDate(f["publish_date"])
		if err != nil {
			return f, "Choose the day scholars should see it.", "publish_date"
		}
		m := 7 * 60
		if f["publish_time"] != "" {
			if m, err = cal.ParseClock(f["publish_time"]); err != nil {
				return f, "The time to show it doesn't look right.", "publish_time"
			}
		}
		publishAt = time.Date(d.Year(), d.Month(), d.Day(), m/60, m%60, 0, 0, loc).Unix()
		if publishAt <= now.Unix() {
			publishAt = now.Unix()
		}
	default:
		f["publish"] = "now"
		publishAt = now.Unix()
		if a.Published(now.Unix()) {
			publishAt = a.PublishAt // keep when it first showed
		}
	}
	a.Title, a.Instructions, a.DueDate, a.DueTime, a.PublishAt = title, f["instructions"], dueDate, dueTime, publishAt
	a.WorkOnline, a.AllowFiles = f["work_online"] == "1", f["allow_files"] == "1"
	return f, "", ""
}

func (s *Server) renderAssignForm(w http.ResponseWriter, r *http.Request, status int, d assignFormData) {
	title := "New assignment"
	if !d.IsNew {
		title = "Edit " + d.A.Title
	}
	s.render(w, r, status, "assignment-form", title, "classes", d)
}

func (s *Server) handleAssignmentNewForm(w http.ResponseWriter, r *http.Request) {
	c, ca, ok := s.class(w, r)
	if !ok {
		return
	}
	if !ca.CanEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't add assignments here", "Only the class's mentors and admins can.")
		return
	}
	s.renderAssignForm(w, r, http.StatusOK, assignFormData{IsNew: true, Class: c,
		Form: map[string]string{"publish": "now", "work_online": "1", "allow_files": "1"}})
}

func (s *Server) handleAssignmentCreate(w http.ResponseWriter, r *http.Request) {
	c, ca, ok := s.class(w, r)
	if !ok {
		return
	}
	if !ca.CanEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't add assignments here", "Only the class's mentors and admins can.")
		return
	}
	if err := parseUpload(r); err != nil {
		s.renderAssignForm(w, r, http.StatusUnprocessableEntity, assignFormData{IsNew: true, Class: c,
			Form: map[string]string{"publish": "now", "work_online": "1", "allow_files": "1"}, Error: s.uploadError(r, err)})
		return
	}
	u := current(r).user
	a := &store.Assignment{ClassID: c.ID, CreatedBy: u.ID}
	f, msg, field := s.readAssignment(r, a)
	if msg != "" {
		if hasUploads(r) {
			msg += " Choose the files again too."
		}
		s.renderAssignForm(w, r, http.StatusUnprocessableEntity, assignFormData{IsNew: true, Class: c, Form: f, Error: msg, Field: field})
		return
	}
	if err := s.store.CreateAssignment(a); err != nil {
		s.serverError(w, r, "creating assignment", err)
		return
	}
	slog.Info("assignment created", "by", u.Username, "assignment", a.ID, "class", c.Name)
	if _, err := s.saveUploads(r, store.FileForAssignment, a.ID, 0, u.ID); err != nil {
		s.setFlash(w, r, "error", "The assignment is saved, but not all its files: "+s.uploadError(r, err))
		http.Redirect(w, r, assignmentPath(a.ID)+"/edit", http.StatusSeeOther)
		return
	}
	msg = a.Title + " is saved as a draft. Scholars can't see it until you publish it."
	switch {
	case a.Published(time.Now().Unix()):
		msg = a.Title + " is published. Scholars in " + c.Name + " can see it now."
	case !a.Draft():
		msg = a.Title + " is saved. Scholars see it from " + time.Unix(a.PublishAt, 0).In(s.loc()).Format("Monday, January 2 at 3:04 PM") + "."
	}
	s.redirect(w, r, assignmentPath(a.ID), msg)
}

func hasUploads(r *http.Request) bool {
	if r.MultipartForm == nil {
		return false
	}
	for _, fh := range r.MultipartForm.File["files"] {
		if fh.Filename != "" && fh.Size > 0 {
			return true
		}
	}
	return false
}

func (s *Server) handleAssignmentEditForm(w http.ResponseWriter, r *http.Request) {
	a, acc, ok := s.assignment(w, r)
	if !ok {
		return
	}
	if !acc.CanEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't change this assignment", "Only the class's mentors and admins can.")
		return
	}
	files, _ := s.store.ListFiles(store.FileForAssignment, a.ID)
	s.renderAssignForm(w, r, http.StatusOK, assignFormData{Class: acc.Class, A: a, Form: assignForm(a, s.loc()), Files: fileItems(files)})
}

func (s *Server) handleAssignmentUpdate(w http.ResponseWriter, r *http.Request) {
	a, acc, ok := s.assignment(w, r)
	if !ok {
		return
	}
	if !acc.CanEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't change this assignment", "Only the class's mentors and admins can.")
		return
	}
	files, _ := s.store.ListFiles(store.FileForAssignment, a.ID)
	if err := parseUpload(r); err != nil {
		s.renderAssignForm(w, r, http.StatusUnprocessableEntity, assignFormData{Class: acc.Class, A: a, Form: assignForm(a, s.loc()),
			Files: fileItems(files), Error: s.uploadError(r, err)})
		return
	}
	updated := *a
	f, msg, field := s.readAssignment(r, &updated)
	if msg != "" {
		if hasUploads(r) {
			msg += " Choose the files again too."
		}
		s.renderAssignForm(w, r, http.StatusUnprocessableEntity, assignFormData{Class: acc.Class, A: a, Form: f, Files: fileItems(files),
			Error: msg, Field: field})
		return
	}
	if err := s.store.UpdateAssignment(&updated); err != nil {
		s.serverError(w, r, "saving assignment", err)
		return
	}
	if _, err := s.saveUploads(r, store.FileForAssignment, a.ID, len(files), current(r).user.ID); err != nil {
		s.setFlash(w, r, "error", "Saved, but not all the files: "+s.uploadError(r, err))
		http.Redirect(w, r, assignmentPath(a.ID)+"/edit", http.StatusSeeOther)
		return
	}
	s.redirect(w, r, assignmentPath(a.ID), "Saved.")
}

func (s *Server) handleAssignmentFileDelete(w http.ResponseWriter, r *http.Request) {
	a, acc, ok := s.assignment(w, r)
	if !ok {
		return
	}
	if !acc.CanEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't change this assignment", "Only the class's mentors and admins can.")
		return
	}
	f, ok := s.ownedFile(w, r, store.FileForAssignment, a.ID)
	if !ok {
		return
	}
	if err := s.deleteFile(f); err != nil {
		s.serverError(w, r, "removing file", err)
		return
	}
	s.redirect(w, r, assignmentPath(a.ID)+"/edit", f.Name+" is removed.")
}

// ownedFile loads the file {fid} in the URL if it belongs to the owner.
func (s *Server) ownedFile(w http.ResponseWriter, r *http.Request, kind string, ownerID int64) (*store.File, bool) {
	fid, err := strconv.ParseInt(r.PathValue("fid"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil, false
	}
	f, err := s.store.GetFile(fid)
	if err != nil || f.OwnerKind != kind || f.OwnerID != ownerID {
		s.notFound(w, r)
		return nil, false
	}
	return f, true
}

func (s *Server) deleteFile(f *store.File) error {
	stored, err := s.store.DeleteFile(f.ID)
	if err != nil {
		return err
	}
	s.removeStored(stored)
	return nil
}

func (s *Server) handleAssignmentDelete(w http.ResponseWriter, r *http.Request) {
	a, acc, ok := s.assignment(w, r)
	if !ok {
		return
	}
	if !acc.CanEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't change this assignment", "Only the class's mentors and admins can.")
		return
	}
	if err := s.store.DeleteAssignment(a.ID); err != nil {
		s.serverError(w, r, "deleting assignment", err)
		return
	}
	s.cleanFiles()
	slog.Info("assignment deleted", "by", current(r).user.Username, "assignment", a.ID, "class", acc.Class.Name)
	s.redirect(w, r, classPath(a.ClassID)+"/assignments", a.Title+" is deleted, with all the work turned in for it.")
}

// ------------------------------------------------------------ scholars' work

// scholarWork checks that the signed-in scholar can work on the assignment
// now, and returns their work (started if needed).
func (s *Server) scholarWork(w http.ResponseWriter, r *http.Request) (*store.Assignment, *store.Submission, bool) {
	a, acc, ok := s.assignment(w, r)
	if !ok {
		return nil, nil, false
	}
	if !acc.Scholar {
		s.renderError(w, r, http.StatusForbidden, "Only scholars turn in work", "Scholars in the class turn in their work here.")
		return nil, nil, false
	}
	if acc.Class.Archived {
		s.renderError(w, r, http.StatusForbidden, "This class is archived", "Work can't be changed in an archived class.")
		return nil, nil, false
	}
	wk, err := s.store.StartSubmission(a.ID, current(r).user.ID)
	if err != nil {
		s.serverError(w, r, "starting work", err)
		return nil, nil, false
	}
	return a, wk, true
}

const notEditable = "Your work is turned in, so it can't be changed. Take it back first if your mentor hasn't looked at it yet."

// handleWork saves a scholar's work, with any new files, and turns it in if
// asked.
func (s *Server) handleWork(w http.ResponseWriter, r *http.Request) {
	if err := parseUpload(r); err != nil {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		s.setFlash(w, r, "error", s.uploadError(r, err))
		http.Redirect(w, r, assignmentPath(id), http.StatusSeeOther)
		return
	}
	a, wk, ok := s.scholarWork(w, r)
	if !ok {
		return
	}
	back := assignmentPath(a.ID) + "#work"
	if !wk.Editable() {
		s.setFlash(w, r, "error", notEditable)
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	u := current(r).user
	if a.WorkOnline {
		body := strings.TrimRight(strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"), " \n")
		if utf8.RuneCountInString(body) > maxWork {
			s.setFlash(w, r, "error", "Your writing is too long to save. Keep it under 100,000 characters, or attach it as a file.")
			http.Redirect(w, r, back, http.StatusSeeOther)
			return
		}
		if err := s.store.SaveWork(wk.ID, body); err != nil {
			s.serverError(w, r, "saving work", err)
			return
		}
		wk.Body = body
	}
	added := 0
	if a.AllowFiles {
		have, _ := s.store.ListFiles(store.FileForSubmission, wk.ID)
		n, err := s.saveUploads(r, store.FileForSubmission, wk.ID, len(have), u.ID)
		if err != nil {
			s.setFlash(w, r, "error", s.uploadError(r, err))
			http.Redirect(w, r, back, http.StatusSeeOther)
			return
		}
		added = n
	}
	if r.PostFormValue("action") != "turn_in" {
		msg := "Saved."
		if added > 0 {
			msg = "Saved, with " + plural(added, "new file") + "."
		}
		s.redirect(w, r, back, msg+" It's not turned in yet.")
		return
	}
	if a.WorkOnline || a.AllowFiles {
		files, _ := s.store.ListFiles(store.FileForSubmission, wk.ID)
		if strings.TrimSpace(wk.Body) == "" && len(files) == 0 {
			s.setFlash(w, r, "error", "There's nothing to turn in yet. Write your work or attach a file first.")
			http.Redirect(w, r, back, http.StatusSeeOther)
			return
		}
	}
	if err := s.store.SetWorkStatus(wk.ID, []string{store.WorkDraft, store.WorkNeedsWork}, store.WorkTurnedIn, u.ID, nil); err != nil {
		if errors.Is(err, store.ErrWrongStatus) {
			s.setFlash(w, r, "error", notEditable)
			http.Redirect(w, r, back, http.StatusSeeOther)
			return
		}
		s.serverError(w, r, "turning in work", err)
		return
	}
	slog.Info("work turned in", "by", u.Username, "assignment", a.ID)
	s.redirect(w, r, back, "Turned in. Your mentor will look at it.")
}

// handleWorkAutosave saves the writing while a scholar types. It answers
// with JSON for the page's script.
func (s *Server) handleWorkAutosave(w http.ResponseWriter, r *http.Request) {
	reply := func(status int, v map[string]string) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		reply(http.StatusNotFound, map[string]string{"error": "Not found."})
		return
	}
	a, err := s.store.GetAssignment(id)
	if err != nil {
		reply(http.StatusNotFound, map[string]string{"error": "Not found."})
		return
	}
	u := current(r).user
	acc, err := s.assignmentAccess(u, a)
	if err != nil || !acc.CanSee || !acc.Scholar || acc.Class.Archived || !a.WorkOnline {
		reply(http.StatusForbidden, map[string]string{"error": "This work can't be saved here."})
		return
	}
	body := strings.TrimRight(strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"), " \n")
	if utf8.RuneCountInString(body) > maxWork {
		reply(http.StatusRequestEntityTooLarge, map[string]string{"error": "Too long to save."})
		return
	}
	wk, err := s.store.StartSubmission(a.ID, u.ID)
	if err != nil {
		s.logError(r, "starting work", err)
		reply(http.StatusInternalServerError, map[string]string{"error": "Not saved."})
		return
	}
	if !wk.Editable() {
		reply(http.StatusConflict, map[string]string{"error": "Already turned in."})
		return
	}
	if wk.Body != body {
		if err := s.store.SaveWork(wk.ID, body); err != nil {
			s.logError(r, "saving work", err)
			reply(http.StatusInternalServerError, map[string]string{"error": "Not saved."})
			return
		}
	}
	reply(http.StatusOK, map[string]string{"saved": time.Now().In(s.loc()).Format("3:04 PM")})
}

func (s *Server) handleWorkTakeBack(w http.ResponseWriter, r *http.Request) {
	a, wk, ok := s.scholarWork(w, r)
	if !ok {
		return
	}
	back := assignmentPath(a.ID) + "#work"
	err := s.store.SetWorkStatus(wk.ID, []string{store.WorkTurnedIn}, store.WorkDraft, current(r).user.ID, nil)
	if errors.Is(err, store.ErrWrongStatus) {
		s.setFlash(w, r, "error", "It can't be taken back now: your mentor has already looked at it.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err != nil {
		s.serverError(w, r, "taking back work", err)
		return
	}
	s.redirect(w, r, back, "Taken back. Make your changes, then turn it in again.")
}

func (s *Server) handleWorkFileDelete(w http.ResponseWriter, r *http.Request) {
	a, wk, ok := s.scholarWork(w, r)
	if !ok {
		return
	}
	back := assignmentPath(a.ID) + "#work"
	if !wk.Editable() {
		s.setFlash(w, r, "error", notEditable)
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	f, ok := s.ownedFile(w, r, store.FileForSubmission, wk.ID)
	if !ok {
		return
	}
	if err := s.deleteFile(f); err != nil {
		s.serverError(w, r, "removing file", err)
		return
	}
	s.redirect(w, r, back, f.Name+" is removed.")
}

// ------------------------------------------------------------ review

type reviewData struct {
	A        *store.Assignment
	Access   assignAccess
	Scholar  *store.User
	Work     *store.Submission
	Body     template.HTML
	Files    []fileItem
	State    workState
	Due      string
	History  []store.HistoryItem
	Prev     *store.Member // the scholars before and after, for moving through the class
	Next     *store.Member
	Feedback string
}

// review loads the assignment and scholar for a review page.
func (s *Server) review(w http.ResponseWriter, r *http.Request) (*store.Assignment, assignAccess, *store.User, bool) {
	a, acc, ok := s.assignment(w, r)
	if !ok {
		return nil, acc, nil, false
	}
	if !acc.CanReview {
		s.notFound(w, r)
		return nil, acc, nil, false
	}
	sid, err := strconv.ParseInt(r.PathValue("sid"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil, acc, nil, false
	}
	sch, err := s.store.GetUser(sid)
	if err != nil {
		s.notFound(w, r)
		return nil, acc, nil, false
	}
	role, _ := s.store.ClassRole(a.ClassID, sid)
	if role != store.ClassScholar {
		// Someone who left the class can still have work here.
		if _, err := s.store.GetSubmission(a.ID, sid); err != nil {
			s.notFound(w, r)
			return nil, acc, nil, false
		}
	}
	return a, acc, sch, true
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	a, acc, sch, ok := s.review(w, r)
	if !ok {
		return
	}
	loc := s.loc()
	d := reviewData{A: a, Access: acc, Scholar: sch, Due: dueText(a, loc, s.today())}
	wk, err := s.store.GetSubmission(a.ID, sch.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, "loading work", err)
		return
	}
	if err == nil {
		d.Work, d.Feedback = wk, wk.Feedback
		if wk.Body != "" {
			d.Body, _ = markdown.Render([]byte(wk.Body), markdown.Options{HardWraps: true})
		}
		list, _ := s.store.ListFiles(store.FileForSubmission, wk.ID)
		d.Files = fileItems(list)
		d.History, _ = s.store.SubmissionHistory(wk.ID)
	}
	d.State = stateOf(a, d.Work, loc, time.Now())
	if members, err := s.store.ClassMembers(a.ClassID, false); err == nil {
		var scholars []*store.Member
		for _, m := range members {
			if m.ClassRole == store.ClassScholar {
				scholars = append(scholars, m)
			}
		}
		for i, m := range scholars {
			if m.ID == sch.ID {
				if i > 0 {
					d.Prev = scholars[i-1]
				}
				if i < len(scholars)-1 {
					d.Next = scholars[i+1]
				}
			}
		}
	}
	s.render(w, r, http.StatusOK, "review", sch.DisplayName+": "+a.Title, "classes", d)
}

// handleReviewSave records a mentor's feedback and decision.
func (s *Server) handleReviewSave(w http.ResponseWriter, r *http.Request) {
	a, acc, sch, ok := s.review(w, r)
	if !ok {
		return
	}
	if acc.Class.Archived && !current(r).user.IsLeader() {
		s.renderError(w, r, http.StatusForbidden, "This class is archived", "Work can't be changed in an archived class.")
		return
	}
	u := current(r).user
	feedback := strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("feedback"), "\r\n", "\n"))
	back := assignmentPath(a.ID) + "/work/" + strconv.FormatInt(sch.ID, 10)
	if utf8.RuneCountInString(feedback) > 20000 {
		s.setFlash(w, r, "error", "The feedback is too long. Keep it to 20,000 characters.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	switch r.PostFormValue("decision") {
	case "unsubmit", "undo_complete":
		s.handleReviewUndo(w, r, a, sch, back)
		return
	}
	var to string
	switch r.PostFormValue("decision") {
	case "complete":
		to = store.WorkComplete
	case "needs_work":
		to = store.WorkNeedsWork
		if feedback == "" {
			s.setFlash(w, r, "error", "Say what needs another look, so "+sch.DisplayName+" knows what to change.")
			http.Redirect(w, r, back, http.StatusSeeOther)
			return
		}
	default:
		s.notFound(w, r)
		return
	}
	wk, err := s.store.StartSubmission(a.ID, sch.ID)
	if err != nil {
		s.serverError(w, r, "loading work", err)
		return
	}
	all := []string{store.WorkDraft, store.WorkTurnedIn, store.WorkNeedsWork, store.WorkComplete}
	if err := s.store.SetWorkStatus(wk.ID, all, to, u.ID, &feedback); err != nil {
		s.serverError(w, r, "saving feedback", err)
		return
	}
	slog.Info("work reviewed", "by", u.Username, "assignment", a.ID, "scholar", sch.Username, "status", to)
	msg := sch.DisplayName + "'s work is marked Complete."
	if to == store.WorkNeedsWork {
		msg = sch.DisplayName + " will see your feedback and can turn it in again."
	}
	// Go on to the next scholar waiting for review, if there is one.
	next := assignmentPath(a.ID) + "#roster"
	if work, err := s.store.ListSubmissions(a.ID); err == nil {
		if members, err := s.store.ClassMembers(a.ClassID, false); err == nil {
			for _, m := range members {
				if wk := work[m.ID]; wk != nil && wk.Status == store.WorkTurnedIn && m.ID != sch.ID && m.ClassRole == store.ClassScholar {
					next = assignmentPath(a.ID) + "/work/" + strconv.FormatInt(m.ID, 10)
					msg += " Here's the next piece of work to look at."
					break
				}
			}
		}
	}
	s.redirect(w, r, next, msg)
}

// handleReviewUndo lets a mentor take back a decision without feedback:
// "unsubmit" returns turned-in or complete work to the scholar as not turned
// in; "undo_complete" puts complete work back to waiting for review.
func (s *Server) handleReviewUndo(w http.ResponseWriter, r *http.Request, a *store.Assignment, sch *store.User, back string) {
	u := current(r).user
	wk, err := s.store.GetSubmission(a.ID, sch.ID)
	if err != nil {
		s.setFlash(w, r, "error", sch.DisplayName+" hasn't turned anything in.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	var from []string
	var to, kind, msg string
	if r.PostFormValue("decision") == "undo_complete" {
		from, kind, msg = []string{store.WorkComplete}, store.WorkUncompleted, "It's no longer marked complete: it's turned in, waiting for review."
		to = store.WorkTurnedIn
		if wk.TurnedInAt == 0 { // marked complete without being turned in
			to, msg = store.WorkDraft, "It's no longer marked complete, and not turned in."
		}
	} else {
		from, to, kind = []string{store.WorkTurnedIn, store.WorkComplete}, store.WorkDraft, store.WorkReturned
		msg = "Returned to " + sch.DisplayName + " as not turned in. They can change it and turn it in again."
	}
	err = s.store.MoveWork(wk.ID, from, to, kind, u.ID, nil)
	if errors.Is(err, store.ErrWrongStatus) {
		s.setFlash(w, r, "error", "That doesn't apply to this work any more. It may have just changed; here it is now.")
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err != nil {
		s.serverError(w, r, "changing work status", err)
		return
	}
	slog.Info("work status undone", "by", u.Username, "assignment", a.ID, "scholar", sch.Username, "status", to)
	s.redirect(w, r, back, msg)
}

// ------------------------------------------------------------ home page

// homeAssignments lists assignments across all of u's classes for the home
// page: for a scholar, work still to do (past due first, then by due date);
// for a mentor, work waiting for review, then what's due in the next two weeks.
func (s *Server) homeAssignments(u *store.User, classes []*store.Class, limit int) (todo, mentored []assignItem) {
	var asScholar, asMentor []int64
	for _, c := range classes {
		if c.MyRole == store.ClassMentor {
			asMentor = append(asMentor, c.ID)
		} else {
			asScholar = append(asScholar, c.ID)
		}
	}
	now := time.Now().Unix()
	if len(asScholar) > 0 {
		list, err := s.store.ListAssignments(store.AssignmentQuery{ClassIDs: asScholar, PublishedOnly: true, Now: now})
		if err != nil {
			slog.Error("listing assignments", "err", err)
		}
		for _, it := range s.assignItems(list, u, func(*store.Assignment) bool { return false }) {
			if k := it.State.Kind; (k == "todo" || k == "late" || k == "needs") && len(todo) < limit {
				todo = append(todo, it)
			}
		}
	}
	if len(asMentor) > 0 {
		list, err := s.store.ListAssignments(store.AssignmentQuery{ClassIDs: asMentor})
		if err != nil {
			slog.Error("listing assignments", "err", err)
		}
		today := s.today()
		from, to := cal.FormatDate(today), cal.FormatDate(today.AddDate(0, 0, 14))
		var review, soon []assignItem
		for _, it := range s.assignItems(list, u, func(*store.Assignment) bool { return true }) {
			switch {
			case it.TurnedIn > 0:
				review = append(review, it)
			case !it.Draft() && it.DueDate >= from && it.DueDate <= to:
				soon = append(soon, it)
			}
		}
		mentored = append(review, soon...)
		if len(mentored) > limit {
			mentored = mentored[:limit]
		}
	}
	return todo, mentored
}
