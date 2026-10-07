package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/store"
)

type peopleData struct {
	Users  []*store.User
	Filter store.UserFilter
	Roles  []string
}

func (s *Server) handlePeople(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.UserFilter{Query: q.Get("q"), Role: q.Get("role"), Status: q.Get("status")}
	if !store.ValidRole(f.Role) {
		f.Role = ""
	}
	if f.Status != "inactive" && f.Status != "all" {
		f.Status = "active"
	}
	list, err := s.store.ListUsers(f)
	if err != nil {
		s.serverError(w, r, "listing people", err)
		return
	}
	s.render(w, r, http.StatusOK, "people", "People", "people", peopleData{Users: list, Filter: f, Roles: store.Roles})
}

type personData struct {
	Classes         []*store.Class
	IsNew           bool
	Self            bool
	TwoStepRequired bool
	Person          *store.User
	Form            map[string]string
	Roles           []string
	Error           string
}

func (s *Server) handlePersonNewForm(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")
	if !store.ValidRole(role) {
		role = store.RoleScholar
	}
	s.render(w, r, http.StatusOK, "person", "Add a person", "people",
		personData{IsNew: true, Form: map[string]string{"role": role}, Roles: store.Roles})
}

func (s *Server) handlePersonCreate(w http.ResponseWriter, r *http.Request) {
	form := formValues(r, "display_name", "username", "email", "role")
	fail := func(msg string) {
		s.render(w, r, http.StatusUnprocessableEntity, "person", "Add a person", "people",
			personData{IsNew: true, Form: form, Roles: store.Roles, Error: msg})
	}
	u := &store.User{Role: form["role"], Active: true, MustChangePassword: true}
	if msg := validateProfile(u, form); msg != "" {
		fail(msg)
		return
	}
	if !store.ValidRole(u.Role) {
		fail("Choose a role.")
		return
	}
	temp := auth.TempPassword()
	var err error
	if u.PasswordHash, err = auth.HashPassword(temp); err != nil {
		s.serverError(w, r, "hashing password", err)
		return
	}
	if err := s.store.CreateUser(u); errors.Is(err, store.ErrUsernameTaken) {
		fail("Someone already has the username " + u.Username + ". Choose another.")
		return
	} else if err != nil {
		s.serverError(w, r, "adding person", err)
		return
	}
	slog.Info("person added", "by", current(r).user.Username, "username", u.Username, "role", u.Role)
	s.renderTempPassword(w, r, http.StatusCreated, u, temp, u.DisplayName+" is added")
}

type tempPasswordData struct {
	Heading  string
	Person   *store.User
	Password string
	URL      string
}

func (s *Server) renderTempPassword(w http.ResponseWriter, r *http.Request, status int, u *store.User, temp, heading string) {
	s.render(w, r, status, "temp-password", heading, "people",
		tempPasswordData{Heading: heading, Person: u, Password: temp, URL: s.baseURL(r)})
}

// person loads the person named in the URL, writing a 404 if there's none.
func (s *Server) person(w http.ResponseWriter, r *http.Request) *store.User {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return nil
	}
	u, err := s.store.GetUser(id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil
	}
	if err != nil {
		s.serverError(w, r, "loading person", err)
		return nil
	}
	return u
}

func personForm(u *store.User) map[string]string {
	active := ""
	if u.Active {
		active = "1"
	}
	return map[string]string{"display_name": u.DisplayName, "username": u.Username, "email": u.Email, "role": u.Role, "active": active}
}

func (s *Server) handlePersonForm(w http.ResponseWriter, r *http.Request) {
	u := s.person(w, r)
	if u == nil {
		return
	}
	classes, err := s.store.ListClassesFor(u.ID)
	if err != nil {
		s.serverError(w, r, "listing classes", err)
		return
	}
	s.render(w, r, http.StatusOK, "person", u.DisplayName, "people",
		personData{Person: u, Self: u.ID == current(r).user.ID, Form: personForm(u), Roles: store.Roles,
			TwoStepRequired: s.mfaRequired(u), Classes: classes})
}

func (s *Server) handlePersonUpdate(w http.ResponseWriter, r *http.Request) {
	orig := s.person(w, r)
	if orig == nil {
		return
	}
	self := orig.ID == current(r).user.ID
	form := formValues(r, "display_name", "username", "email", "role", "active")
	fail := func(msg string) {
		s.render(w, r, http.StatusUnprocessableEntity, "person", orig.DisplayName, "people",
			personData{Person: orig, Self: self, Form: form, Roles: store.Roles, Error: msg})
	}
	u := *orig
	if msg := validateProfile(&u, form); msg != "" {
		fail(msg)
		return
	}
	u.Role, u.Active = form["role"], form["active"] == "1"
	if self { // admins can't demote or lock out themselves
		u.Role, u.Active = orig.Role, true
		form["role"], form["active"] = orig.Role, "1"
	}
	if !store.ValidRole(u.Role) {
		fail("Choose a role.")
		return
	}
	if orig.IsAdmin() && orig.Active && (!u.IsAdmin() || !u.Active) {
		n, err := s.store.ActiveAdmins()
		if err != nil {
			s.serverError(w, r, "counting admins", err)
			return
		}
		if n <= 1 {
			fail(orig.DisplayName + " is the only admin. Make someone else an admin first.")
			return
		}
	}
	if err := s.store.UpdateUser(&u); errors.Is(err, store.ErrUsernameTaken) {
		fail("Someone already has the username " + u.Username + ". Choose another.")
		return
	} else if err != nil {
		s.serverError(w, r, "saving person", err)
		return
	}
	if !u.Active && orig.Active {
		if err := s.store.DeleteUserSessions(u.ID, ""); err != nil {
			s.logError(r, "signing out deactivated person", err)
		}
		slog.Info("person deactivated", "by", current(r).user.Username, "username", u.Username)
	}
	msg := "Saved."
	switch {
	case !u.Active && orig.Active:
		msg = u.DisplayName + " can't sign in anymore. Their work is kept."
	case u.Active && !orig.Active:
		msg = u.DisplayName + " can sign in again."
	}
	s.redirect(w, r, "/admin/people/"+strconv.FormatInt(u.ID, 10), msg)
}

func (s *Server) handlePersonResetPassword(w http.ResponseWriter, r *http.Request) {
	u := s.person(w, r)
	if u == nil {
		return
	}
	if u.ID == current(r).user.ID {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Change your own password instead",
			"To change your own password, go to My account.")
		return
	}
	temp := auth.TempPassword()
	hash, err := auth.HashPassword(temp)
	if err != nil {
		s.serverError(w, r, "hashing password", err)
		return
	}
	if err := s.store.SetPassword(u.ID, hash, true); err != nil {
		s.serverError(w, r, "resetting password", err)
		return
	}
	if err := s.store.DeleteUserSessions(u.ID, ""); err != nil {
		s.logError(r, "signing out after password reset", err)
	}
	s.userThrottle.Reset(u.Username)
	slog.Info("password reset", "by", current(r).user.Username, "username", u.Username)
	s.renderTempPassword(w, r, http.StatusOK, u, temp, "New temporary password for "+u.DisplayName)
}
