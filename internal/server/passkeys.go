package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bradyloveland/taper/internal/passkey"
	"github.com/bradyloveland/taper/internal/store"
)

// Passkeys sign people in with their phone's or computer's fingerprint, face
// or PIN, instead of a password (and code). Each passkey belongs to the
// address it was made on, like learn.example.org. Because the device checks
// who's using it, a passkey counts as two-step sign-in.

const (
	passkeyTimeout = 2 * time.Minute
	maxChallenges  = 10000
)

// pkChallenge is a challenge waiting for a passkey's answer.
type pkChallenge struct {
	userID  int64  // 0 when signing in
	session string // the session adding a passkey
	expires time.Time
}

// newChallenge makes a random challenge for a ceremony.
func (s *Server) newChallenge(userID int64, session string) ([]byte, bool) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, false
	}
	s.pkMu.Lock()
	defer s.pkMu.Unlock()
	if s.pkChallenges == nil {
		s.pkChallenges = map[string]pkChallenge{}
	}
	now := time.Now()
	if len(s.pkChallenges) >= maxChallenges {
		for k, c := range s.pkChallenges {
			if now.After(c.expires) {
				delete(s.pkChallenges, k)
			}
		}
		if len(s.pkChallenges) >= maxChallenges {
			return nil, false
		}
	}
	s.pkChallenges[string(b)] = pkChallenge{userID: userID, session: session, expires: now.Add(passkeyTimeout)}
	return b, true
}

// takeChallenge uses up the challenge in a passkey's client data, returning
// it if it's one this server gave out and hasn't expired.
func (s *Server) takeChallenge(clientData []byte) ([]byte, pkChallenge, bool) {
	var cd struct {
		Challenge string `json:"challenge"`
	}
	if json.Unmarshal(clientData, &cd) != nil {
		return nil, pkChallenge{}, false
	}
	ch, err := passkey.Decode(cd.Challenge)
	if err != nil {
		return nil, pkChallenge{}, false
	}
	s.pkMu.Lock()
	defer s.pkMu.Unlock()
	c, ok := s.pkChallenges[string(ch)]
	delete(s.pkChallenges, string(ch))
	if !ok || time.Now().After(c.expires) {
		return nil, pkChallenge{}, false
	}
	return ch, c, true
}

// origin is the address the browser is at, as WebAuthn sees it, and the
// passkeys' site name (its host, without a port).
func (s *Server) origin(r *http.Request) (origin, rpID string) {
	scheme := "http"
	if s.secure(r) {
		scheme = "https"
	}
	host := r.Host
	if s.netcfg(r).Trusted(r.RemoteAddr) {
		if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
			host = strings.TrimSpace(strings.Split(fh, ",")[0])
		}
	}
	rpID = host
	if h, _, err := net.SplitHostPort(host); err == nil {
		rpID = h
	}
	return scheme + "://" + host, rpID
}

func formBytes(r *http.Request, name string) []byte {
	b, err := passkey.Decode(r.PostFormValue(name))
	if err != nil {
		return nil
	}
	return b
}

// ------------------------------------------------------------- adding

type passkeysData struct {
	Passkeys []*store.Passkey
	Required bool // the school requires two-step sign-in for this person
}

func (s *Server) handlePasskeys(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	list, err := s.store.UserPasskeys(u.ID)
	if err != nil {
		s.serverError(w, r, "listing passkeys", err)
		return
	}
	s.render(w, r, http.StatusOK, "passkeys", "Passkeys", "account",
		passkeysData{Passkeys: list, Required: s.mfaRequired(u) && !u.TOTPEnabled})
}

// handlePasskeyOptions starts adding a passkey: what the browser needs to
// make one.
func (s *Server) handlePasskeyOptions(w http.ResponseWriter, r *http.Request) {
	ri := current(r)
	u := ri.user
	handle, err := s.store.PasskeyHandle(u.ID)
	if err != nil {
		s.logError(r, "making passkey handle", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong. Try again."})
		return
	}
	ch, ok := s.newChallenge(u.ID, ri.sess.TokenHash)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "The server is busy. Try again in a minute."})
		return
	}
	have, _ := s.store.UserPasskeys(u.ID)
	exclude := []map[string]string{}
	for _, p := range have {
		exclude = append(exclude, map[string]string{"type": "public-key", "id": p.CredID})
	}
	params := []map[string]any{}
	for _, a := range passkey.Algorithms {
		params = append(params, map[string]any{"type": "public-key", "alg": a})
	}
	_, rpID := s.origin(r)
	school, _ := s.appNames()
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge":          passkey.B64.EncodeToString(ch),
		"rp":                 map[string]string{"id": rpID, "name": "Taper · " + school},
		"user":               map[string]string{"id": handle, "name": u.Username, "displayName": u.DisplayName},
		"pubKeyCredParams":   params,
		"excludeCredentials": exclude,
		"authenticatorSelection": map[string]any{
			"residentKey": "required", "requireResidentKey": true, "userVerification": "required"},
		"attestation": "none",
		"timeout":     passkeyTimeout.Milliseconds(),
	})
}

// handlePasskeyAdd saves a passkey the browser made.
func (s *Server) handlePasskeyAdd(w http.ResponseWriter, r *http.Request) {
	ri := current(r)
	u := ri.user
	fail := func(msg string, err error) {
		if err != nil {
			slog.Warn("passkey not added", "user", u.Username, "err", err)
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
	}
	reg := passkey.Registration{ID: formBytes(r, "id"), ClientData: formBytes(r, "clientData"), AuthData: formBytes(r, "authData"),
		PublicKey: formBytes(r, "publicKey")}
	reg.Alg, _ = strconv.Atoi(r.PostFormValue("alg"))
	ch, c, ok := s.takeChallenge(reg.ClientData)
	if !ok || c.userID != u.ID || c.session != ri.sess.TokenHash {
		fail("That took too long. Try again.", nil)
		return
	}
	origin, rpID := s.origin(r)
	cred, err := passkey.Register(reg, passkey.Expect{Challenge: ch, Origin: origin, RPID: rpID})
	if err != nil {
		fail("That passkey couldn't be added. Try again, or try another device.", err)
		return
	}
	p := &store.Passkey{UserID: u.ID, CredID: passkey.B64.EncodeToString(cred.ID), PublicKey: cred.PublicKey, Alg: cred.Alg,
		SignCount: cred.SignCount, BackedUp: cred.BackedUp, Name: device(r.UserAgent())}
	if err := s.store.AddPasskey(p); errors.Is(err, store.ErrPasskeyTaken) {
		fail("This passkey is already added.", nil)
		return
	} else if err != nil {
		s.logError(r, "saving passkey", err)
		fail("Something went wrong. Try again.", nil)
		return
	}
	slog.Info("passkey added", "user", u.Username, "name", p.Name)
	s.setFlash(w, r, "ok", "Your passkey is added. Next time, sign in with it: no password needed.")
	writeJSON(w, http.StatusOK, map[string]string{"next": "/account/passkeys"})
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ok, err := s.store.DeletePasskey(u.ID, id)
	if err != nil {
		s.serverError(w, r, "removing passkey", err)
		return
	}
	if !ok {
		s.notFound(w, r)
		return
	}
	slog.Info("passkey removed", "user", u.Username)
	s.redirect(w, r, "/account/passkeys", "The passkey is removed from your account. You can also delete it from the device's passwords.")
}

// handlePersonPasskeysOff removes someone's passkeys, for a lost device.
func (s *Server) handlePersonPasskeysOff(w http.ResponseWriter, r *http.Request) {
	u := s.manageable(w, r)
	if u == nil {
		return
	}
	n, err := s.store.DeleteUserPasskeys(u.ID)
	if err != nil {
		s.serverError(w, r, "removing passkeys", err)
		return
	}
	slog.Info("passkeys removed", "by", current(r).user.Username, "user", u.Username, "count", n)
	s.redirect(w, r, "/admin/people/"+strconv.FormatInt(u.ID, 10), u.DisplayName+"'s passkeys are removed. They sign in with their password now.")
}

// ------------------------------------------------------------- signing in

// handlePasskeyLoginOptions starts signing in with a passkey.
func (s *Server) handlePasskeyLoginOptions(w http.ResponseWriter, r *http.Request) {
	if wait := s.ipThrottle.Blocked(s.clientIP(r)); wait > 0 {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": waitMessage(wait)})
		return
	}
	ch, ok := s.newChallenge(0, "")
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "The server is busy. Try again in a minute."})
		return
	}
	_, rpID := s.origin(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"challenge": passkey.B64.EncodeToString(ch), "rpId": rpID, "userVerification": "required",
		"timeout": passkeyTimeout.Milliseconds(),
	})
}

// handlePasskeyLogin signs someone in with a passkey.
func (s *Server) handlePasskeyLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if wait := s.ipThrottle.Blocked(ip); wait > 0 {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": waitMessage(wait)})
		return
	}
	origin, rpID := s.origin(r)
	fail := func(status int, msg string, err error) {
		if err != nil {
			slog.Warn("passkey sign-in refused", "ip", ip, "err", err)
		}
		if status == http.StatusUnauthorized {
			s.ipThrottle.Fail(ip)
		}
		writeJSON(w, status, map[string]string{"error": msg})
	}
	if o := r.Header.Get("Origin"); o != "" && o != origin {
		fail(http.StatusForbidden, "Sign in from Taper's own page.", nil)
		return
	}
	a := passkey.Assertion{ID: formBytes(r, "id"), ClientData: formBytes(r, "clientData"), AuthData: formBytes(r, "authData"),
		Signature: formBytes(r, "signature"), UserHandle: formBytes(r, "userHandle")}
	ch, c, ok := s.takeChallenge(a.ClientData)
	if !ok || c.userID != 0 {
		fail(http.StatusBadRequest, "That took too long. Try again.", nil)
		return
	}
	const didnt = "That passkey didn't work. It may have been removed from your account. Sign in with your password instead."
	p, err := s.store.PasskeyByCredID(passkey.B64.EncodeToString(a.ID))
	if err != nil {
		fail(http.StatusUnauthorized, didnt, err)
		return
	}
	u, err := s.store.GetUser(p.UserID)
	if err != nil {
		fail(http.StatusUnauthorized, didnt, err)
		return
	}
	if len(a.UserHandle) > 0 {
		if h, _ := s.store.PasskeyHandle(u.ID); passkey.B64.EncodeToString(a.UserHandle) != h {
			fail(http.StatusUnauthorized, didnt, errors.New("user handle doesn't match"))
			return
		}
	}
	cred := &passkey.Credential{ID: a.ID, PublicKey: p.PublicKey, Alg: p.Alg, SignCount: p.SignCount}
	count, err := passkey.Verify(a, cred, passkey.Expect{Challenge: ch, Origin: origin, RPID: rpID})
	if err != nil {
		fail(http.StatusUnauthorized, didnt, err)
		return
	}
	if !u.Active {
		fail(http.StatusForbidden, "This account is turned off. Ask an admin at your school to turn it back on.", nil)
		return
	}
	if err := s.store.UsePasskey(p.ID, count); err != nil {
		s.logError(r, "recording passkey use", err)
	}
	if !s.startSession(w, r, u, r.PostFormValue("remember") == "1") {
		return
	}
	if err := s.store.TouchLogin(u.ID); err != nil {
		s.logError(r, "recording sign-in", err)
	}
	slog.Info("signed in with a passkey", "user", u.Username)
	next := safeNext(r.PostFormValue("next"))
	if u.MustChangePassword {
		next = "/password"
	}
	writeJSON(w, http.StatusOK, map[string]string{"next": next})
}

// hasPasskey reports whether u has a passkey (which counts as two-step
// sign-in).
func (s *Server) hasPasskey(u *store.User) bool {
	n, err := s.store.CountPasskeys(u.ID)
	return err == nil && n > 0
}
