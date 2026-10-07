package server

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bradyloveland/taper/internal/markdown"
	"github.com/bradyloveland/taper/internal/store"
)

// classAccess is what the signed-in person may do with a class.
type classAccess struct {
	Role        string // their role in the class, or ""
	CanSee      bool
	CanEdit     bool // details and scholars: admins and the class's mentors
	CanMentors  bool // mentors, archiving, deleting: admins
	ShowAccount bool // usernames and links to people: admins and mentors
}

func (s *Server) access(u *store.User, c *store.Class) (classAccess, error) {
	role, err := s.store.ClassRole(c.ID, u.ID)
	if err != nil {
		return classAccess{}, err
	}
	a := classAccess{Role: role}
	a.CanSee = u.IsMentor() || role != ""
	a.CanEdit = u.IsAdmin() || (role == store.ClassMentor && !c.Archived)
	a.CanMentors = u.IsAdmin()
	a.ShowAccount = u.IsMentor()
	return a, nil
}

// class loads the class in the URL that the signed-in person may see,
// writing an error page if there's none.
func (s *Server) class(w http.ResponseWriter, r *http.Request) (*store.Class, classAccess, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil, classAccess{}, false
	}
	c, err := s.store.GetClass(id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil, classAccess{}, false
	}
	if err != nil {
		s.serverError(w, r, "loading class", err)
		return nil, classAccess{}, false
	}
	a, err := s.access(current(r).user, c)
	if err != nil {
		s.serverError(w, r, "checking class access", err)
		return nil, classAccess{}, false
	}
	if !a.CanSee {
		// Don't reveal that the class exists.
		s.notFound(w, r)
		return nil, classAccess{}, false
	}
	return c, a, true
}

func classPath(id int64) string { return "/classes/" + strconv.FormatInt(id, 10) }

// --------------------------------------------------------------- listing

type classesData struct {
	Mine     []*store.Class
	All      []*store.Class
	Filter   store.ClassFilter
	Terms    []string
	ShowAll  bool // admins and mentors see every class
	Archived int
}

func (s *Server) handleClasses(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	q := r.URL.Query()
	d := classesData{Filter: store.ClassFilter{Term: q.Get("term"), Query: q.Get("q"), Archived: q.Get("archived") == "1"},
		ShowAll: u.IsMentor()}
	var err error
	if d.Mine, err = s.store.ListClassesFor(u.ID); err != nil {
		s.serverError(w, r, "listing classes", err)
		return
	}
	if d.ShowAll {
		if d.All, err = s.store.ListClasses(d.Filter); err != nil {
			s.serverError(w, r, "listing classes", err)
			return
		}
		d.Terms, _ = s.store.Terms()
		if arch, err := s.store.ListClasses(store.ClassFilter{Archived: true}); err == nil {
			d.Archived = len(arch)
		}
	}
	s.render(w, r, http.StatusOK, "classes", "Classes", "classes", d)
}

// --------------------------------------------------------------- one class

type classData struct {
	Upcoming    []occurrence
	Assignments []assignItem
	NAssign     int // all the assignments the viewer can see
	Reviewer    bool
	Class       *store.Class
	Access      classAccess
	Description template.HTML
	Mentors     []*store.Member
	Scholars    []*store.Member
}

func splitMembers(list []*store.Member) (mentors, scholars []*store.Member) {
	for _, m := range list {
		if m.ClassRole == store.ClassMentor {
			mentors = append(mentors, m)
		} else {
			scholars = append(scholars, m)
		}
	}
	return
}

func (s *Server) handleClass(w http.ResponseWriter, r *http.Request) {
	c, a, ok := s.class(w, r)
	if !ok {
		return
	}
	members, err := s.store.ClassMembers(c.ID, false)
	if err != nil {
		s.serverError(w, r, "listing members", err)
		return
	}
	d := classData{Class: c, Access: a}
	d.Mentors, d.Scholars = splitMembers(members)
	d.Upcoming = s.upcoming(store.EventQuery{ClassIDs: []int64{c.ID}}, 31, 6)
	u := current(r).user
	d.Reviewer = u.IsAdmin() || a.Role == store.ClassMentor
	d.Assignments, d.NAssign = s.classAssignments(c, a, u, 6)
	if c.Description != "" {
		d.Description, _ = markdown.Render([]byte(c.Description), markdown.Options{})
	}
	s.render(w, r, http.StatusOK, "class", c.Name, "classes", d)
}

// ------------------------------------------------------------- the form

type classFormData struct {
	IsNew  bool
	Class  *store.Class
	Access classAccess
	Form   map[string]string
	Colors []string
	Terms  []string
	Error  string
}

func classForm(c *store.Class) map[string]string {
	return map[string]string{"name": c.Name, "description": c.Description, "term": c.Term, "meets": c.Meets, "color": c.Color}
}

// readClass validates the class form into c.
func readClass(r *http.Request, c *store.Class) (map[string]string, string) {
	form := formValues(r, "name", "term", "meets", "color")
	form["description"] = strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("description"), "\r\n", "\n"))
	name, msg := cleanName(form["name"], "class name")
	if msg != "" {
		return form, msg
	}
	if utf8.RuneCountInString(form["term"]) > 40 || utf8.RuneCountInString(form["meets"]) > 100 {
		return form, "Keep the term to 40 characters and the meeting time to 100."
	}
	if utf8.RuneCountInString(form["description"]) > 20000 {
		return form, "The description is too long. Keep it to 20,000 characters."
	}
	if !store.ValidColor(form["color"]) {
		form["color"] = "blue"
	}
	c.Name, c.Term, c.Meets, c.Color, c.Description = name, strings.Join(strings.Fields(form["term"]), " "),
		strings.Join(strings.Fields(form["meets"]), " "), form["color"], form["description"]
	return form, ""
}

func (s *Server) handleClassNewForm(w http.ResponseWriter, r *http.Request) {
	terms, _ := s.store.Terms()
	term := ""
	if len(terms) > 0 {
		term = terms[0]
	}
	s.render(w, r, http.StatusOK, "class-form", "New class", "classes", classFormData{IsNew: true,
		Form: map[string]string{"color": "blue", "term": term}, Colors: store.ClassColors, Terms: terms})
}

func (s *Server) handleClassCreate(w http.ResponseWriter, r *http.Request) {
	c := &store.Class{}
	form, msg := readClass(r, c)
	if msg != "" {
		terms, _ := s.store.Terms()
		s.render(w, r, http.StatusUnprocessableEntity, "class-form", "New class", "classes", classFormData{IsNew: true,
			Form: form, Colors: store.ClassColors, Terms: terms, Error: msg})
		return
	}
	if err := s.store.CreateClass(c); err != nil {
		s.serverError(w, r, "creating class", err)
		return
	}
	slog.Info("class created", "by", current(r).user.Username, "class", c.Name)
	s.redirect(w, r, classPath(c.ID)+"/members", c.Name+" is created. Now add its mentors and scholars.")
}

func (s *Server) handleClassEditForm(w http.ResponseWriter, r *http.Request) {
	c, a, ok := s.class(w, r)
	if !ok {
		return
	}
	if !a.CanEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't change this class", "Only its mentors and admins can.")
		return
	}
	terms, _ := s.store.Terms()
	s.render(w, r, http.StatusOK, "class-form", "Edit "+c.Name, "classes", classFormData{Class: c, Access: a,
		Form: classForm(c), Colors: store.ClassColors, Terms: terms})
}

func (s *Server) handleClassUpdate(w http.ResponseWriter, r *http.Request) {
	c, a, ok := s.class(w, r)
	if !ok {
		return
	}
	if !a.CanEdit {
		s.renderError(w, r, http.StatusForbidden, "You can't change this class", "Only its mentors and admins can.")
		return
	}
	updated := *c
	form, msg := readClass(r, &updated)
	if msg != "" {
		terms, _ := s.store.Terms()
		s.render(w, r, http.StatusUnprocessableEntity, "class-form", "Edit "+c.Name, "classes", classFormData{Class: c, Access: a,
			Form: form, Colors: store.ClassColors, Terms: terms, Error: msg})
		return
	}
	if err := s.store.UpdateClass(&updated); err != nil {
		s.serverError(w, r, "saving class", err)
		return
	}
	s.redirect(w, r, classPath(c.ID), "Saved.")
}

func (s *Server) handleClassArchive(w http.ResponseWriter, r *http.Request) {
	c, a, ok := s.class(w, r)
	if !ok {
		return
	}
	if !a.CanMentors {
		s.renderError(w, r, http.StatusForbidden, "Admins only", "Only admins can archive classes.")
		return
	}
	c.Archived = r.PathValue("action") == "archive"
	if err := s.store.UpdateClass(c); err != nil {
		s.serverError(w, r, "archiving class", err)
		return
	}
	msg := c.Name + " is archived. It's kept, read-only, under Archived classes."
	if !c.Archived {
		msg = c.Name + " is back in the current classes."
	}
	slog.Info("class archived", "by", current(r).user.Username, "class", c.Name, "archived", c.Archived)
	s.redirect(w, r, classPath(c.ID), msg)
}

// handleClassAction routes POST /classes/{id}/archive, /unarchive and /delete.
func (s *Server) handleClassAction(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("action") {
	case "archive", "unarchive":
		s.handleClassArchive(w, r)
	case "delete":
		s.handleClassDelete(w, r)
	default:
		s.notFound(w, r)
	}
}

func (s *Server) handleClassDelete(w http.ResponseWriter, r *http.Request) {
	c, a, ok := s.class(w, r)
	if !ok {
		return
	}
	if !a.CanMentors {
		s.renderError(w, r, http.StatusForbidden, "Admins only", "Only admins can delete classes.")
		return
	}
	if !c.Archived {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Archive it first", "Only archived classes can be deleted, so a class isn't deleted by mistake.")
		return
	}
	if strings.TrimSpace(r.PostFormValue("confirm")) != c.Name {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Not deleted", "Type the class's name exactly to delete it.")
		return
	}
	if err := s.store.DeleteClass(c.ID); err != nil {
		s.serverError(w, r, "deleting class", err)
		return
	}
	s.cleanFiles()
	slog.Info("class deleted", "by", current(r).user.Username, "class", c.Name)
	s.redirect(w, r, "/classes?archived=1", c.Name+" is deleted.")
}

// --------------------------------------------------------------- members

type candidate struct {
	*store.User
	Search string // lower-case name and username, for the filter box
}

type membersData struct {
	Class     *store.Class
	Access    classAccess
	Mentors   []*store.Member
	Scholars  []*store.Member
	AddScholr []candidate
	AddMentor []candidate
}

func candidates(users []*store.User, skip map[int64]bool) []candidate {
	var out []candidate
	for _, u := range users {
		if !skip[u.ID] {
			out = append(out, candidate{u, strings.ToLower(u.DisplayName + " " + u.Username)})
		}
	}
	return out
}

func (s *Server) handleClassMembers(w http.ResponseWriter, r *http.Request) {
	c, a, ok := s.class(w, r)
	if !ok {
		return
	}
	if !a.CanEdit {
		http.Redirect(w, r, classPath(c.ID), http.StatusSeeOther)
		return
	}
	members, err := s.store.ClassMembers(c.ID, true)
	if err != nil {
		s.serverError(w, r, "listing members", err)
		return
	}
	in := map[int64]bool{}
	for _, m := range members {
		in[m.ID] = true
	}
	d := membersData{Class: c, Access: a}
	d.Mentors, d.Scholars = splitMembers(members)
	scholars, err := s.store.ListUsers(store.UserFilter{Role: store.RoleScholar})
	if err != nil {
		s.serverError(w, r, "listing people", err)
		return
	}
	d.AddScholr = candidates(scholars, in)
	if a.CanMentors {
		mentors, _ := s.store.ListUsers(store.UserFilter{Role: store.RoleMentor})
		admins, _ := s.store.ListUsers(store.UserFilter{Role: store.RoleAdmin})
		d.AddMentor = candidates(append(mentors, admins...), in)
	}
	s.render(w, r, http.StatusOK, "class-members", "Members of "+c.Name, "classes", d)
}

func (s *Server) handleClassMembersAdd(w http.ResponseWriter, r *http.Request) {
	c, a, ok := s.class(w, r)
	if !ok {
		return
	}
	role := r.PostFormValue("role")
	if !a.CanEdit || (role == store.ClassMentor && !a.CanMentors) || (role != store.ClassMentor && role != store.ClassScholar) {
		s.renderError(w, r, http.StatusForbidden, "You can't change this class", "Only admins choose mentors; mentors and admins choose scholars.")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.serverError(w, r, "reading form", err)
		return
	}
	var ids []int64
	for _, v := range r.PostForm["user"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		u, err := s.store.GetUser(id)
		if err != nil || !u.Active {
			continue
		}
		// Mentors come from admins and mentors, scholars from scholars.
		if (role == store.ClassMentor) != u.IsMentor() {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		s.setFlash(w, r, "error", "Choose at least one person to add.")
		http.Redirect(w, r, classPath(c.ID)+"/members", http.StatusSeeOther)
		return
	}
	n, err := s.store.AddMembers(c.ID, role, ids)
	if err != nil {
		s.serverError(w, r, "adding members", err)
		return
	}
	slog.Info("class members added", "by", current(r).user.Username, "class", c.Name, "role", role, "count", n)
	s.redirect(w, r, classPath(c.ID)+"/members", plural(n, role)+" added.")
}

func (s *Server) handleClassMemberRemove(w http.ResponseWriter, r *http.Request) {
	c, a, ok := s.class(w, r)
	if !ok {
		return
	}
	uid, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	role, err := s.store.ClassRole(c.ID, uid)
	if err != nil {
		s.serverError(w, r, "checking member", err)
		return
	}
	if !a.CanEdit || (role == store.ClassMentor && !a.CanMentors) {
		s.renderError(w, r, http.StatusForbidden, "You can't change this class", "Only admins choose mentors; mentors and admins choose scholars.")
		return
	}
	if err := s.store.RemoveMember(c.ID, uid); err != nil {
		s.serverError(w, r, "removing member", err)
		return
	}
	name := "They"
	if u, err := s.store.GetUser(uid); err == nil {
		name = u.DisplayName
	}
	s.redirect(w, r, classPath(c.ID)+"/members", name+" is no longer in "+c.Name+".")
}
