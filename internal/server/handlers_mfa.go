package server

import (
	"crypto/subtle"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/qr"
	"github.com/bradyloveland/taper/internal/store"
)

// recoveryCount is how many recovery codes each person gets.
const recoveryCount = 10

// settingRequireMFA lists the roles that must use two-step sign-in.
const settingRequireMFA = "require_mfa"

// mfaPolicy says which roles must use two-step sign-in.
type mfaPolicy struct {
	Admin   bool `json:"admin"`
	Board   bool `json:"board"`
	Mentor  bool `json:"mentor"`
	Scholar bool `json:"scholar"`
}

func (s *Server) mfaPolicy() mfaPolicy {
	var p mfaPolicy
	_, _ = s.store.GetSetting(settingRequireMFA, &p)
	return p
}

// mfaRequired reports whether u's role must use two-step sign-in.
func (s *Server) mfaRequired(u *store.User) bool {
	p := s.mfaPolicy()
	switch u.Role {
	case store.RoleAdmin:
		return p.Admin
	case store.RoleBoard:
		return p.Board
	case store.RoleMentor:
		return p.Mentor
	case store.RoleScholar:
		return p.Scholar
	}
	return false
}

var totpSecretRE = regexp.MustCompile(`^[A-Z2-7]{32}$`)

// looksLikeTOTP tells a six-digit code from a recovery code.
func looksLikeTOTP(code string) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// checkSecondStep checks a code from the authenticator app or a recovery
// code for u. It reports whether it was right and whether a recovery code was used.
func (s *Server) checkSecondStep(u *store.User, code string) (ok, recovery bool, err error) {
	if looksLikeTOTP(code) {
		secret, err := s.box.Open(u.TOTPSecret)
		if err != nil {
			return false, false, err
		}
		step, match := auth.TOTPMatch(secret, code, u.TOTPLastStep, time.Now())
		if !match {
			return false, false, nil
		}
		ok, err := s.store.UseTOTPStep(u.ID, step)
		return ok, false, err
	}
	norm := auth.NormalizeRecovery(code)
	if len(norm) != 10 {
		return false, false, nil
	}
	ok, err = s.store.UseRecoveryCode(u.ID, auth.HashRecovery(code))
	return ok, true, err
}

// ------------------------------------------------------------ signing in

type verifyData struct {
	Next  string
	Error string
	CSRF  string
}

// pendingSession returns the request's session if it's waiting for its
// second step.
func pendingSession(r *http.Request) *reqInfo {
	ri := current(r)
	if ri.user == nil || ri.sess == nil || !ri.sess.MFAPending {
		return nil
	}
	return ri
}

func (s *Server) handleVerifyForm(w http.ResponseWriter, r *http.Request) {
	if pendingSession(r) == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "verify", "Two-step sign-in", "", verifyData{Next: r.URL.Query().Get("next")})
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	ri := pendingSession(r)
	if ri == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(ri.sess.CSRF)) != 1 {
		s.renderError(w, r, http.StatusForbidden, "That form expired", "Go back, reload the page and try again.")
		return
	}
	d := verifyData{Next: r.PostFormValue("next")}
	key := "mfa:" + strconv.FormatInt(ri.user.ID, 10)
	if wait := s.mfaThrottle.Blocked(key); wait > 0 {
		_ = s.store.DeleteSession(ri.sess.TokenHash)
		s.clearSessionCookie(w, r)
		s.setFlash(w, r, "error", "Too many wrong codes. "+waitMessage(wait)[len("Too many tries. "):])
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	ok, recovery, err := s.checkSecondStep(ri.user, r.PostFormValue("code"))
	if err != nil {
		s.serverError(w, r, "checking two-step code", err)
		return
	}
	if !ok {
		s.mfaThrottle.Fail(key)
		d.Error = "That code isn't right. Codes change every 30 seconds; use the newest one."
		if !looksLikeTOTP(r.PostFormValue("code")) {
			d.Error = "That recovery code isn't right, or it was already used."
		}
		s.render(w, r, http.StatusUnauthorized, "verify", "Two-step sign-in", "", d)
		return
	}
	s.mfaThrottle.Reset(key)
	if err := s.store.CompleteMFA(ri.sess.TokenHash); err != nil {
		s.serverError(w, r, "finishing sign-in", err)
		return
	}
	if ri.sess.Remember {
		if c, err := r.Cookie(sessionCookie); err == nil {
			s.setSessionCookie(w, r, c.Value, true)
		}
	}
	if err := s.store.TouchLogin(ri.user.ID); err != nil {
		s.logError(r, "recording sign-in", err)
	}
	if recovery {
		left, _ := s.store.RecoveryCodesLeft(ri.user.ID)
		slog.Info("signed in with a recovery code", "username", ri.user.Username, "left", left)
		msg := fmt.Sprintf("You used a recovery code. You have %d left.", left)
		if left <= 3 {
			msg += " Make new ones under My account → Two-step sign-in."
		}
		s.setFlash(w, r, "ok", msg)
	}
	http.Redirect(w, r, safeNext(d.Next), http.StatusSeeOther)
}

// ------------------------------------------------------------- my account

type twoStepData struct {
	Enabled  bool
	Required bool
	Left     int
	Secret   string        // during setup
	Grouped  string        // the secret in groups of four, to type by hand
	QR       template.HTML // during setup
	Error    string
	Codes    []string // new recovery codes, shown once
}

func (s *Server) twoStepSetup(u *store.User, secret string) (twoStepData, error) {
	if secret == "" {
		secret = auth.NewTOTPSecret()
	}
	name, _ := s.appNames()
	svg, err := qr.SVG(auth.TOTPURI(secret, name, u.Username))
	if err != nil {
		return twoStepData{}, err
	}
	return twoStepData{Secret: secret, Grouped: auth.GroupSecret(secret), QR: template.HTML(svg), Required: s.mfaRequired(u)}, nil
}

func (s *Server) renderTwoStep(w http.ResponseWriter, r *http.Request, status int, d twoStepData) {
	u := current(r).user
	d.Enabled, d.Required = u.TOTPEnabled, s.mfaRequired(u)
	if d.Enabled {
		d.Left, _ = s.store.RecoveryCodesLeft(u.ID)
	}
	s.render(w, r, status, "two-step", "Two-step sign-in", "account", d)
}

func (s *Server) handleTwoStep(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	if u.TOTPEnabled {
		s.renderTwoStep(w, r, http.StatusOK, twoStepData{})
		return
	}
	d, err := s.twoStepSetup(u, "")
	if err != nil {
		s.serverError(w, r, "making QR code", err)
		return
	}
	s.renderTwoStep(w, r, http.StatusOK, d)
}

// newRecoveryCodes makes codes and returns them with their hashes.
func newRecoveryCodes() ([]string, []string) {
	codes := auth.NewRecoveryCodes(recoveryCount)
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = auth.HashRecovery(c)
	}
	return codes, hashes
}

func (s *Server) handleTwoStepEnable(w http.ResponseWriter, r *http.Request) {
	ri := current(r)
	secret := strings.TrimSpace(r.PostFormValue("secret"))
	if !totpSecretRE.MatchString(secret) || ri.user.TOTPEnabled {
		http.Redirect(w, r, "/account/two-step", http.StatusSeeOther)
		return
	}
	step, ok := auth.TOTPMatch(secret, r.PostFormValue("code"), 0, time.Now())
	if !ok {
		d, err := s.twoStepSetup(ri.user, secret)
		if err != nil {
			s.serverError(w, r, "making QR code", err)
			return
		}
		d.Error = "That code isn't right. Check the time on your phone is set automatically, and enter the newest code."
		s.renderTwoStep(w, r, http.StatusUnprocessableEntity, d)
		return
	}
	codes, hashes := newRecoveryCodes()
	if err := s.store.EnableTOTP(ri.user.ID, s.box.Seal(secret), step, hashes); err != nil {
		s.serverError(w, r, "turning on two-step sign-in", err)
		return
	}
	// Other browsers signed in with just a password are signed out.
	if err := s.store.DeleteUserSessions(ri.user.ID, ri.sess.TokenHash); err != nil {
		s.logError(r, "signing out other sessions", err)
	}
	slog.Info("two-step sign-in turned on", "username", ri.user.Username)
	ri.user.TOTPEnabled = true
	s.renderTwoStep(w, r, http.StatusOK, twoStepData{Codes: codes})
}

// checkOwnPassword confirms the signed-in person's password for a
// sensitive change, with throttling. It writes the error page if not.
func (s *Server) checkOwnPassword(w http.ResponseWriter, r *http.Request) bool {
	ri := current(r)
	ip := s.clientIP(r)
	if d := max(s.ipThrottle.Blocked(ip), s.userThrottle.Blocked(ri.user.Username)); d > 0 {
		s.renderTwoStep(w, r, http.StatusTooManyRequests, twoStepData{Error: waitMessage(d)})
		return false
	}
	if !auth.VerifyPassword(r.PostFormValue("password"), ri.user.PasswordHash) {
		s.ipThrottle.Fail(ip)
		s.userThrottle.Fail(ri.user.Username)
		s.renderTwoStep(w, r, http.StatusUnprocessableEntity, twoStepData{Error: "Your password isn't right. Type it again."})
		return false
	}
	return true
}

func (s *Server) handleTwoStepRecovery(w http.ResponseWriter, r *http.Request) {
	ri := current(r)
	if !ri.user.TOTPEnabled {
		http.Redirect(w, r, "/account/two-step", http.StatusSeeOther)
		return
	}
	if !s.checkOwnPassword(w, r) {
		return
	}
	codes, hashes := newRecoveryCodes()
	if err := s.store.ReplaceRecoveryCodes(ri.user.ID, hashes); err != nil {
		s.serverError(w, r, "making recovery codes", err)
		return
	}
	slog.Info("new recovery codes made", "username", ri.user.Username)
	s.renderTwoStep(w, r, http.StatusOK, twoStepData{Codes: codes})
}

func (s *Server) handleTwoStepDisable(w http.ResponseWriter, r *http.Request) {
	ri := current(r)
	if !ri.user.TOTPEnabled {
		http.Redirect(w, r, "/account/two-step", http.StatusSeeOther)
		return
	}
	if s.mfaRequired(ri.user) {
		s.renderTwoStep(w, r, http.StatusUnprocessableEntity, twoStepData{Error: "Your school requires two-step sign-in for " +
			strings.ToLower(ri.user.RoleLabel()) + "s, so it can't be turned off."})
		return
	}
	if !s.checkOwnPassword(w, r) {
		return
	}
	if err := s.store.DisableTOTP(ri.user.ID); err != nil {
		s.serverError(w, r, "turning off two-step sign-in", err)
		return
	}
	slog.Info("two-step sign-in turned off", "username", ri.user.Username)
	s.redirect(w, r, "/account/two-step", "Two-step sign-in is off. You sign in with just your password now.")
}

// ---------------------------------------------------------------- admins

// handlePersonTwoStepOff turns off someone's two-step sign-in, for a person
// who lost their phone and recovery codes.
func (s *Server) handlePersonTwoStepOff(w http.ResponseWriter, r *http.Request) {
	u := s.manageable(w, r)
	if u == nil {
		return
	}
	if u.ID == current(r).user.ID {
		s.renderError(w, r, http.StatusUnprocessableEntity, "Use My account instead", "To change your own two-step sign-in, go to My account.")
		return
	}
	if err := s.store.DisableTOTP(u.ID); err != nil {
		s.serverError(w, r, "turning off two-step sign-in", err)
		return
	}
	if err := s.store.DeleteUserSessions(u.ID, ""); err != nil {
		s.logError(r, "signing out", err)
	}
	slog.Info("two-step sign-in turned off by an admin", "by", current(r).user.Username, "username", u.Username)
	msg := "Two-step sign-in is off for " + u.DisplayName + "."
	if s.mfaRequired(u) {
		msg += " They'll be asked to set it up again when they next sign in."
	}
	s.redirect(w, r, "/admin/people/"+strconv.FormatInt(u.ID, 10), msg)
}

func (s *Server) handleSecuritySave(w http.ResponseWriter, r *http.Request) {
	p := mfaPolicy{Admin: r.PostFormValue("admin") == "1", Board: r.PostFormValue("board") == "1", Mentor: r.PostFormValue("mentor") == "1",
		Scholar: r.PostFormValue("scholar") == "1"}
	if err := s.store.SetSetting(settingRequireMFA, p); err != nil {
		s.serverError(w, r, "saving security settings", err)
		return
	}
	slog.Info("two-step sign-in policy saved", "by", current(r).user.Username, "admin", p.Admin, "board", p.Board, "mentor", p.Mentor, "scholar", p.Scholar)
	msg := "Saved."
	if s.mfaRequired(current(r).user) && !current(r).user.TOTPEnabled {
		msg = "Saved. Set up two-step sign-in for yourself now."
	}
	s.redirect(w, r, "/admin/settings#security", msg)
}
