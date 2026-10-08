package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/mail"
	"github.com/bradyloveland/taper/internal/store"
	"github.com/bradyloveland/taper/internal/update"
)

// Email settings. The password is stored separately, encrypted.
const (
	settingSMTP         = "smtp"
	settingSMTPPassword = "smtp_password"
	settingAdminAlerts  = "email_admin_alerts"
	settingNotifiedVer  = "notified_update_version"
	settingNotifiedRoll = "notified_rollback_at"
)

// mailConfig returns the email settings, with the password.
func (s *Server) mailConfig() mail.Config {
	var c mail.Config
	_, _ = s.store.GetSetting(settingSMTP, &c)
	var sealed string
	if ok, _ := s.store.GetSetting(settingSMTPPassword, &sealed); ok {
		if pw, err := s.box.Open(sealed); err == nil {
			c.Password = pw
		}
	}
	if c.FromName == "" {
		c.FromName, _ = s.appNames()
	}
	return c
}

func (s *Server) emailReady() bool { c := s.mailConfig(); return c.Ready() }

func (s *Server) adminAlerts() bool {
	on := true
	_, _ = s.store.GetSetting(settingAdminAlerts, &on)
	return on
}

// sendMail sends one email now.
func (s *Server) sendMail(ctx context.Context, to []string, subject, body string) error {
	return mail.Send(ctx, s.mailConfig(), mail.Message{To: to, Subject: subject, Body: body})
}

// notifyAdmins emails every admin who has an address, in the background,
// if email and admin notices are turned on.
func (s *Server) notifyAdmins(subject, body string) {
	if !s.emailReady() || !s.adminAlerts() {
		return
	}
	admins, err := s.store.ListAdmins()
	if err != nil {
		slog.Error("listing admins to email", "err", err)
		return
	}
	var to []string
	for _, a := range admins {
		if a.Email != "" {
			to = append(to, a.Email)
		}
	}
	if len(to) == 0 {
		return
	}
	school, _ := s.appNames()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		full := body + "\n\n-- \nTaper at " + school + "\nAdmins get these notices. Turn them off under Settings → Email.\n"
		if err := s.sendMail(ctx, to, subject, full); err != nil {
			slog.Warn("emailing admins", "subject", subject, "err", err)
		}
	}()
}

// checkUpdateNotices tells admins, once, about a new version or an update
// that was undone because it didn't start.
func (s *Server) checkUpdateNotices() {
	base := s.publicURL()
	link := func(path string) string {
		if base == "" {
			return ""
		}
		return "\n\n" + base + path
	}
	if v := s.availableUpdate(); v != "" {
		var told string
		_, _ = s.store.GetSetting(settingNotifiedVer, &told)
		if told != v {
			_ = s.store.SetSetting(settingNotifiedVer, v)
			s.notifyAdmins("Taper "+v+" is available",
				"A new version of Taper is out. An admin can read what's new and install it on the Updates page."+link("/admin/updates"))
		}
	}
	if st, err := update.ReadState(s.cfg.DataDir); err == nil && st != nil && st.Phase == update.RolledBack && !st.Manual {
		var told int64
		_, _ = s.store.GetSetting(settingNotifiedRoll, &told)
		if told != st.At {
			_ = s.store.SetSetting(settingNotifiedRoll, st.At)
			s.notifyAdmins("A Taper update was undone",
				fmt.Sprintf("Taper %s didn't work, so %s was put back. %s\n\nYour data is as it was before the update. "+
					"Please report the problem so it can be fixed.%s", st.From, st.To, st.Reason, link("/admin/updates")))
		}
	}
}

// ------------------------------------------------------------- settings

type emailData struct {
	Config     mail.Config
	HasPass    bool
	Alerts     bool
	TestTo     string
	Error      string
	TestResult string
	TestOK     bool
}

func (s *Server) renderEmail(w http.ResponseWriter, r *http.Request, status int, d emailData) {
	if d.Config.Host == "" && d.Error == "" {
		_, _ = s.store.GetSetting(settingSMTP, &d.Config)
	}
	if d.Config.Port == 0 {
		d.Config.Port = 587
	}
	if d.Config.Security == "" {
		d.Config.Security = mail.StartTLS
	}
	var sealed string
	_, _ = s.store.GetSetting(settingSMTPPassword, &sealed)
	d.HasPass = sealed != ""
	d.Alerts = s.adminAlerts()
	if d.TestTo == "" {
		d.TestTo = current(r).user.Email
	}
	s.render(w, r, status, "email", "Email", "email", d)
}

func (s *Server) handleEmail(w http.ResponseWriter, r *http.Request) {
	s.renderEmail(w, r, http.StatusOK, emailData{})
}

func (s *Server) handleEmailSave(w http.ResponseWriter, r *http.Request) {
	form := formValues(r, "host", "port", "security", "username", "from", "from_name")
	port, _ := strconv.Atoi(form["port"])
	c := mail.Config{Host: strings.ToLower(form["host"]), Port: port, Security: form["security"], Username: form["username"],
		From: form["from"], FromName: strings.Join(strings.Fields(form["from_name"]), " ")}
	alerts := r.PostFormValue("alerts") == "1"
	if r.PostFormValue("clear") == "1" {
		_ = s.store.SetSetting(settingSMTP, mail.Config{})
		_ = s.store.SetSetting(settingSMTPPassword, "")
		s.redirect(w, r, "/admin/email", "Email is turned off.")
		return
	}
	if msg := c.Check(); msg != "" {
		s.renderEmail(w, r, http.StatusUnprocessableEntity, emailData{Config: c, Error: msg})
		return
	}
	if err := s.store.SetSetting(settingSMTP, c); err != nil {
		s.serverError(w, r, "saving email settings", err)
		return
	}
	if pw := r.PostFormValue("password"); pw != "" {
		if err := s.store.SetSetting(settingSMTPPassword, s.box.Seal(pw)); err != nil {
			s.serverError(w, r, "saving email password", err)
			return
		}
	} else if c.Username == "" {
		_ = s.store.SetSetting(settingSMTPPassword, "")
	}
	_ = s.store.SetSetting(settingAdminAlerts, alerts)
	slog.Info("email settings saved", "by", current(r).user.Username, "host", c.Host)
	s.redirect(w, r, "/admin/email", "Email settings saved. Send a test email to check them.")
}

func (s *Server) handleEmailTest(w http.ResponseWriter, r *http.Request) {
	to := strings.TrimSpace(r.PostFormValue("to"))
	d := emailData{TestTo: to}
	if addr, msg := cleanEmail(to); msg != "" || addr == "" {
		d.TestResult = "Enter the address to send the test to."
		s.renderEmail(w, r, http.StatusUnprocessableEntity, d)
		return
	}
	if !s.emailReady() {
		d.TestResult = "Save the email settings first."
		s.renderEmail(w, r, http.StatusUnprocessableEntity, d)
		return
	}
	school, _ := s.appNames()
	err := s.sendMail(r.Context(), []string{to}, "Test email from Taper",
		"This is a test from Taper at "+school+". If you're reading it, email works.\n")
	if err != nil {
		d.TestResult = "It didn't work: " + capitalize(err.Error()) + "."
		s.renderEmail(w, r, http.StatusBadGateway, d)
		return
	}
	d.TestOK, d.TestResult = true, "Sent. Check "+to+"'s inbox, and the spam folder."
	s.renderEmail(w, r, http.StatusOK, d)
}

// ------------------------------------------------------- forgot password

type forgotData struct {
	Who   string
	Sent  bool
	Error string
}

func (s *Server) handleForgotForm(w http.ResponseWriter, r *http.Request) {
	if !s.emailReady() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "forgot", "Forgot your password?", "", forgotData{})
}

func (s *Server) handleForgot(w http.ResponseWriter, r *http.Request) {
	if !s.emailReady() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	who := strings.TrimSpace(r.PostFormValue("who"))
	if who == "" {
		s.render(w, r, http.StatusUnprocessableEntity, "forgot", "Forgot your password?", "", forgotData{Error: "Enter your username or email address."})
		return
	}
	ip := s.clientIP(r)
	if s.resetIPLimit.Blocked(ip) > 0 || s.resetThrottle.Blocked(who) > 0 {
		s.render(w, r, http.StatusTooManyRequests, "forgot", "Forgot your password?", "", forgotData{Who: who,
			Error: "Several reset emails were asked for already. Check your inbox (and spam folder), or try again in an hour."})
		return
	}
	s.resetIPLimit.Fail(ip)
	s.resetThrottle.Fail(who)

	var users []*store.User
	if u, err := s.store.GetUserByUsername(who); err == nil && u.Active && u.Email != "" {
		users = append(users, u)
	} else if strings.Contains(who, "@") {
		users, _ = s.store.UsersByEmail(who)
	}
	base := s.baseURL(r)
	school, _ := s.appNames()
	for _, u := range users {
		token, err := s.store.CreatePasswordReset(u.ID)
		if err != nil {
			s.logError(r, "creating reset link", err)
			continue
		}
		body := fmt.Sprintf("Hello %s,\n\nSomeone asked to reset the password for your account (%s) at %s.\n\n"+
			"To choose a new password, open this link within an hour:\n\n%s/reset/%s\n\n"+
			"If you didn't ask for this, ignore this email. Your password stays the same.\n\n-- \nTaper at %s\n",
			u.DisplayName, u.Username, school, base, token, school)
		uu := u
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := s.sendMail(ctx, []string{uu.Email}, "Reset your Taper password", body); err != nil {
				slog.Warn("sending password reset email", "username", uu.Username, "err", err)
			} else {
				slog.Info("password reset email sent", "username", uu.Username)
			}
		}()
	}
	// The same answer whether or not the account exists or has an address.
	s.render(w, r, http.StatusOK, "forgot", "Forgot your password?", "", forgotData{Who: who, Sent: true})
}

type resetData struct {
	Token string
	User  *store.User
	Error string
}

func (s *Server) handleResetForm(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	u, err := s.store.PasswordResetUser(token)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "That link doesn't work", "Reset links work once, for an hour. Ask for a new one from the sign-in page.")
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.render(w, r, http.StatusOK, "reset", "Choose a new password", "", resetData{Token: token, User: u})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	u, err := s.store.PasswordResetUser(token)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "That link doesn't work", "Reset links work once, for an hour. Ask for a new one from the sign-in page.")
		return
	}
	password := r.PostFormValue("password")
	if msg := checkNewPassword(password, r.PostFormValue("confirm")); msg != "" {
		s.render(w, r, http.StatusUnprocessableEntity, "reset", "Choose a new password", "", resetData{Token: token, User: u, Error: msg})
		return
	}
	if ok, err := s.store.UsePasswordReset(token); err != nil || !ok {
		s.renderError(w, r, http.StatusNotFound, "That link doesn't work", "Reset links work once, for an hour. Ask for a new one from the sign-in page.")
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, r, "hashing password", err)
		return
	}
	if err := s.store.SetPassword(u.ID, hash, false); err != nil {
		s.serverError(w, r, "saving password", err)
		return
	}
	if err := s.store.DeleteUserSessions(u.ID, ""); err != nil {
		s.logError(r, "signing out after reset", err)
	}
	s.userThrottle.Reset(u.Username)
	slog.Info("password reset by email", "username", u.Username)
	s.redirect(w, r, "/login", "Your password is changed. Sign in with it now.")
}
