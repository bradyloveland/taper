package server

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/store"
)

// ------------------------------------------------------------------- setup

type setupData struct {
	Form  map[string]string
	Error string
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	if s.setupDone.Load() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if _, err := s.setupCode(); err != nil {
		s.serverError(w, r, "creating setup code", err)
		return
	}
	s.render(w, r, http.StatusOK, "setup", "Set up", "", setupData{Form: map[string]string{}})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.setupDone.Load() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	form := formValues(r, "code", "school", "display_name", "username")
	fail := func(status int, msg string) {
		s.render(w, r, status, "setup", "Set up", "", setupData{Form: form, Error: msg})
	}
	ip := s.clientIP(r)
	if d := s.ipThrottle.Blocked(ip); d > 0 {
		fail(http.StatusTooManyRequests, waitMessage(d))
		return
	}
	code, err := s.setupCode()
	if err != nil {
		s.serverError(w, r, "reading setup code", err)
		return
	}
	if !auth.SameCode(form["code"], code) {
		s.ipThrottle.Fail(ip)
		fail(http.StatusUnprocessableEntity, "That setup code isn't right. Run sudo taper setup-code on the server to see it.")
		return
	}
	school, msg := cleanName(form["school"], "school name")
	if msg != "" {
		fail(http.StatusUnprocessableEntity, msg)
		return
	}
	u := &store.User{Role: store.RoleAdmin, Active: true}
	if msg := validateProfile(u, form); msg != "" {
		fail(http.StatusUnprocessableEntity, msg)
		return
	}
	password := r.PostFormValue("password")
	if msg := checkNewPassword(password, r.PostFormValue("confirm")); msg != "" {
		fail(http.StatusUnprocessableEntity, msg)
		return
	}
	if u.PasswordHash, err = auth.HashPassword(password); err != nil {
		s.serverError(w, r, "hashing password", err)
		return
	}
	if err := s.store.SetSetting("school_name", school); err != nil {
		s.serverError(w, r, "saving school name", err)
		return
	}
	if err := s.store.CreateUser(u); err != nil {
		s.serverError(w, r, "creating first admin", err)
		return
	}
	s.setupDone.Store(true)
	if err := os.Remove(SetupCodePath(s.cfg.DataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("couldn't remove the setup code file", "err", err)
	}
	slog.Info("setup finished", "school", school, "admin", u.Username)
	if !s.startSession(w, r, u, true) {
		return
	}
	s.redirect(w, r, "/", "Taper is ready. Next, add your mentors and scholars.")
}

// ------------------------------------------------------------------- login

type loginData struct {
	Username string
	Next     string
	Remember bool
	Error    string
	CanReset bool // email is set up, so "forgot password" works
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if current(r).user != nil {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", "Sign in", "", loginData{Next: r.URL.Query().Get("next"), CanReset: s.emailReady()})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	d := loginData{Username: username, Next: r.PostFormValue("next"), Remember: r.PostFormValue("remember") == "1", CanReset: s.emailReady()}
	fail := func(status int, msg string) {
		d.Error = msg
		s.render(w, r, status, "login", "Sign in", "", d)
	}
	ip := s.clientIP(r)
	if wait := max(s.ipThrottle.Blocked(ip), s.userThrottle.Blocked(username)); wait > 0 {
		fail(http.StatusTooManyRequests, waitMessage(wait))
		return
	}
	u, err := s.store.GetUserByUsername(username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, "looking up user", err)
		return
	}
	ok := false
	if u != nil {
		ok = auth.VerifyPassword(password, u.PasswordHash)
	} else {
		auth.BurnTime(password)
	}
	if !ok {
		s.ipThrottle.Fail(ip)
		if username != "" {
			s.userThrottle.Fail(username)
		}
		fail(http.StatusUnauthorized, "That username and password don't match. Check them and try again.")
		return
	}
	if !u.Active {
		fail(http.StatusForbidden, "This account is turned off. Ask an admin at your school to turn it back on.")
		return
	}
	s.userThrottle.Reset(username)
	if u.TOTPEnabled {
		if old := current(r).sess; old != nil {
			_ = s.store.DeleteSession(old.TokenHash)
		}
		token, _, err := s.store.CreatePendingSession(u.ID, d.Remember, r.UserAgent(), s.clientIP(r))
		if err != nil {
			s.serverError(w, r, "starting sign-in", err)
			return
		}
		s.setSessionCookie(w, r, token, false)
		target := "/login/verify"
		if next := safeNext(d.Next); next != "/" {
			target += "?next=" + url.QueryEscape(next)
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	if !s.startSession(w, r, u, d.Remember) {
		return
	}
	if err := s.store.TouchLogin(u.ID); err != nil {
		s.logError(r, "recording sign-in", err)
	}
	if u.MustChangePassword {
		http.Redirect(w, r, "/password", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, safeNext(d.Next), http.StatusSeeOther)
}

// startSession signs u in on this browser. It reports false (after writing
// an error page) if it couldn't.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User, remember bool) bool {
	// Replace any session this browser already had.
	if old := current(r).sess; old != nil {
		_ = s.store.DeleteSession(old.TokenHash)
	}
	token, _, err := s.store.CreateSession(u.ID, remember, r.UserAgent(), s.clientIP(r))
	if err != nil {
		s.serverError(w, r, "creating session", err)
		return false
	}
	s.setSessionCookie(w, r, token, remember)
	return true
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSession(current(r).sess.TokenHash); err != nil {
		s.logError(r, "deleting session", err)
	}
	s.clearSessionCookie(w, r)
	s.redirect(w, r, "/login", "You're signed out.")
}

// ---------------------------------------------------------- new password

type passwordData struct{ Error string }

func (s *Server) handlePasswordForm(w http.ResponseWriter, r *http.Request) {
	if !current(r).user.MustChangePassword {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "password", "Choose a new password", "", passwordData{})
}

// handlePassword sets the person's own password after a temporary one.
func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	ri := current(r)
	if !ri.user.MustChangePassword {
		http.Redirect(w, r, "/account", http.StatusSeeOther)
		return
	}
	password := r.PostFormValue("password")
	if msg := checkNewPassword(password, r.PostFormValue("confirm")); msg != "" {
		s.render(w, r, http.StatusUnprocessableEntity, "password", "Choose a new password", "", passwordData{Error: msg})
		return
	}
	if auth.VerifyPassword(password, ri.user.PasswordHash) {
		s.render(w, r, http.StatusUnprocessableEntity, "password", "Choose a new password", "",
			passwordData{Error: "Choose a password that's different from the temporary one."})
		return
	}
	if !s.savePassword(w, r, ri, password) {
		return
	}
	s.redirect(w, r, "/", "Your password is saved. Welcome to Taper!")
}

// savePassword stores a new password for the signed-in person and signs
// their other browsers out.
func (s *Server) savePassword(w http.ResponseWriter, r *http.Request, ri *reqInfo, password string) bool {
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, r, "hashing password", err)
		return false
	}
	if err := s.store.SetPassword(ri.user.ID, hash, false); err != nil {
		s.serverError(w, r, "saving password", err)
		return false
	}
	if err := s.store.DeleteUserSessions(ri.user.ID, ri.sess.TokenHash); err != nil {
		s.logError(r, "signing out other sessions", err)
	}
	return true
}

func checkNewPassword(password, confirm string) string {
	if msg := auth.CheckPassword(password); msg != "" {
		return msg
	}
	if password != confirm {
		return "The two passwords don't match. Type them again."
	}
	return ""
}
