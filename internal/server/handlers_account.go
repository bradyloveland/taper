package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/store"
	"github.com/bradyloveland/taper/internal/version"
)

type homeData struct {
	Upcoming []occurrence
	Greeting string
	Classes  []*store.Class // the person's own classes
	NClasses int            // current classes at the school, for admins
	Counts   map[string]int
	Update   string // a newer version, for admins
	Unsent   int    // problem reports not on GitHub, for admins
}

func greeting(t time.Time) string {
	switch h := t.Hour(); {
	case h < 12:
		return "Good morning"
	case h < 17:
		return "Good afternoon"
	default:
		return "Good evening"
	}
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	d := homeData{Greeting: greeting(time.Now())}
	var err error
	if d.Classes, err = s.store.ListClassesFor(current(r).user.ID); err != nil {
		s.serverError(w, r, "listing classes", err)
		return
	}
	q := store.EventQuery{School: true}
	for _, c := range d.Classes {
		q.ClassIDs = append(q.ClassIDs, c.ID)
	}
	d.Upcoming = s.upcoming(q, 14, 8)
	if current(r).user.IsAdmin() {
		d.NClasses, _ = s.store.CountClasses()
		counts, err := s.store.RoleCounts()
		if err != nil {
			s.serverError(w, r, "counting people", err)
			return
		}
		d.Counts = counts
		d.Update = s.availableUpdate()
		d.Unsent, _ = s.store.UnsentBugReports()
	}
	s.render(w, r, http.StatusOK, "home", "Home", "home", d)
}

// ----------------------------------------------------------------- account

type accountData struct {
	Sessions      []*store.Session
	Current       string
	ProfileError  string
	PasswordError string
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, status int, d accountData) {
	ri := current(r)
	list, err := s.store.UserSessions(ri.user.ID)
	if err != nil {
		s.serverError(w, r, "listing sessions", err)
		return
	}
	d.Sessions, d.Current = list, ri.sess.TokenHash
	s.render(w, r, status, "account", "My account", "account", d)
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, r, http.StatusOK, accountData{})
}

func (s *Server) handleAccountProfile(w http.ResponseWriter, r *http.Request) {
	u := *current(r).user
	form := formValues(r, "display_name", "email")
	var msg string
	if u.DisplayName, msg = cleanName(form["display_name"], "name"); msg == "" {
		u.Email, msg = cleanEmail(form["email"])
	}
	if msg != "" {
		s.renderAccount(w, r, http.StatusUnprocessableEntity, accountData{ProfileError: msg})
		return
	}
	if err := s.store.UpdateUser(&u); err != nil {
		s.serverError(w, r, "saving profile", err)
		return
	}
	s.redirect(w, r, "/account", "Your profile is saved.")
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	ri := current(r)
	fail := func(status int, msg string) {
		s.renderAccount(w, r, status, accountData{PasswordError: msg})
	}
	ip := s.clientIP(r)
	if d := max(s.ipThrottle.Blocked(ip), s.userThrottle.Blocked(ri.user.Username)); d > 0 {
		fail(http.StatusTooManyRequests, waitMessage(d))
		return
	}
	if !auth.VerifyPassword(r.PostFormValue("current"), ri.user.PasswordHash) {
		s.ipThrottle.Fail(ip)
		s.userThrottle.Fail(ri.user.Username)
		fail(http.StatusUnprocessableEntity, "Your current password isn't right. Type it again.")
		return
	}
	password := r.PostFormValue("password")
	if msg := checkNewPassword(password, r.PostFormValue("confirm")); msg != "" {
		fail(http.StatusUnprocessableEntity, msg)
		return
	}
	if !s.savePassword(w, r, ri, password) {
		return
	}
	s.redirect(w, r, "/account", "Your password is changed. You're signed out on your other devices.")
}

func (s *Server) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	ri := current(r)
	if err := s.store.DeleteUserSessions(ri.user.ID, ri.sess.TokenHash); err != nil {
		s.serverError(w, r, "signing out other sessions", err)
		return
	}
	s.redirect(w, r, "/account", "You're signed out everywhere else.")
}

// ---------------------------------------------------------------- settings

type settingsErrors struct {
	School  string
	Zone    string
	Reports string
}

type settingsData struct {
	Errors      settingsErrors
	Network     string
	DataDir     string
	DBSize      string
	Schema      int
	ReportRepo  string
	DefaultRepo string
	HasToken    bool
	Reports     int
	Unsent      int
	MFA         mfaPolicy
	Email       bool
	TimeZone    string   // the setting, or "" for the server's
	ServerZone  string   // the server's own zone, as a label
	Zones       []string // suggestions
	Now         string   // the time now at the school, to check the zone
}

func (s *Server) renderSettingsWith(w http.ResponseWriter, r *http.Request, status int, errs settingsErrors) {
	d := settingsData{Errors: errs, Network: s.currentNet().Describe(), DataDir: s.cfg.DataDir,
		ReportRepo: s.reportRepo(), DefaultRepo: version.Repo, HasToken: s.reportToken() != ""}
	d.Schema, _ = s.store.SchemaVersion()
	var size int64
	matches, _ := filepath.Glob(filepath.Join(s.cfg.DataDir, "taper.db*"))
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil {
			size += fi.Size()
		}
	}
	d.DBSize = humanSize(size)
	if list, err := s.store.ListBugReports(1000); err == nil {
		d.Reports = len(list)
	}
	d.Unsent, _ = s.store.UnsentBugReports()
	d.MFA = s.mfaPolicy()
	_, _ = s.store.GetSetting(settingTimeZone, &d.TimeZone)
	d.ServerZone = time.Now().In(time.Local).Format("MST")
	d.Zones = commonZones
	d.Now = time.Now().In(s.loc()).Format("Monday 3:04 PM")
	d.Email = s.emailReady()
	s.render(w, r, status, "settings", "Settings", "settings", d)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettingsWith(w, r, http.StatusOK, settingsErrors{})
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	school, msg := cleanName(r.PostFormValue("school"), "school name")
	if msg != "" {
		s.renderSettingsWith(w, r, http.StatusUnprocessableEntity, settingsErrors{School: msg})
		return
	}
	zone := strings.TrimSpace(r.PostFormValue("time_zone"))
	if zone != "" {
		if _, err := time.LoadLocation(zone); err != nil || zone == "Local" {
			s.renderSettingsWith(w, r, http.StatusUnprocessableEntity, settingsErrors{School: "That time zone isn't one Taper knows. Use a name like America/Denver or Europe/London."})
			return
		}
	}
	if err := s.store.SetSetting("school_name", school); err != nil {
		s.serverError(w, r, "saving settings", err)
		return
	}
	if err := s.store.SetSetting(settingTimeZone, zone); err != nil {
		s.serverError(w, r, "saving settings", err)
		return
	}
	s.redirect(w, r, "/admin/settings", "Settings saved.")
}

func humanSize(n int64) string {
	units := []string{"bytes", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return plural(int(n), "byte")
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0") + " " + units[i]
}

// commonZones are suggested in the time zone box; any IANA name works.
var commonZones = []string{
	"America/New_York", "America/Chicago", "America/Denver", "America/Phoenix", "America/Los_Angeles",
	"America/Anchorage", "Pacific/Honolulu", "America/Toronto", "America/Vancouver", "America/Mexico_City",
	"America/Sao_Paulo", "Europe/London", "Europe/Dublin", "Europe/Paris", "Europe/Berlin", "Europe/Madrid",
	"Africa/Johannesburg", "Africa/Nairobi", "Asia/Dubai", "Asia/Kolkata", "Asia/Singapore", "Asia/Manila",
	"Asia/Tokyo", "Australia/Perth", "Australia/Sydney", "Pacific/Auckland", "UTC",
}
