package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/bradyloveland/taper/internal/config"
	"github.com/bradyloveland/taper/internal/store"
)

const sessionCookie = "taper_session"

type ctxKey struct{}

// reqInfo is what's known about the signed-in person for a request.
type reqInfo struct {
	user *store.User
	sess *store.Session
}

func current(r *http.Request) *reqInfo {
	if ri, ok := r.Context().Value(ctxKey{}).(*reqInfo); ok {
		return ri
	}
	return &reqInfo{}
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic serving request", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				http.Error(w, "Something went wrong on the server. Try again, and report a problem if it keeps happening.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; " +
	"connect-src 'self'; manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

// maxForm is the largest request body accepted. (File uploads, when they
// come, get their own limit.)
const maxForm = 1 << 20

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(maxForm)
		if r.URL.Path == "/admin/updates/upload" {
			limit = maxUpload
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if s.secure(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// secure reports whether the browser reached us over HTTPS.
func (s *Server) secure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.netcfg(r).Trusted(r.RemoteAddr) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// clientIP is the browser's address, believing X-Forwarded-For only from
// trusted proxies.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	cfg := s.netcfg(r)
	if !cfg.Trusted(r.RemoteAddr) {
		return host
	}
	// Walk from the right, skipping our own proxies.
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.TrimSpace(parts[i])
		if p == "" {
			continue
		}
		if net.ParseIP(p) == nil {
			break
		}
		if !cfg.Trusted(p) {
			return p
		}
		host = p
	}
	return host
}

// baseURL is the address people use to reach this server, for showing in
// instructions.
func (s *Server) baseURL(r *http.Request) string {
	if u := s.publicURL(); u != "" {
		return u
	}
	cfg := s.netcfg(r)
	if cfg.Mode == config.ModeHTTPS {
		return "https://" + cfg.Domain
	}
	scheme := "http"
	if s.secure(r) {
		scheme = "https"
	}
	host := r.Host
	if cfg.Trusted(r.RemoteAddr) {
		if fh := r.Header.Get("X-Forwarded-Host"); fh != "" {
			host = strings.TrimSpace(strings.Split(fh, ",")[0])
		}
	}
	return scheme + "://" + host
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, remember bool) {
	c := &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode}
	if remember {
		c.MaxAge = int(store.RememberFor / time.Second)
	}
	http.SetCookie(w, c)
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure(r), SameSite: http.SameSiteLaxMode})
}

// loadSession attaches the signed-in person, if any, to the request.
func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ri := &reqInfo{}
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			sess, u, err := s.store.LookupSession(c.Value)
			switch {
			case err == nil:
				ri.user, ri.sess = u, sess
				// Keep a remembered cookie alive as long as the session.
				if sess.Remember && sess.LastSeenAt == s.store.Now().Unix() && sess.CreatedAt != sess.LastSeenAt {
					s.setSessionCookie(w, r, c.Value, true)
				}
			case errors.Is(err, store.ErrNotFound):
				s.clearSessionCookie(w, r)
			default:
				s.logError(r, "looking up session", err)
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ri)))
	})
}

// public paths work before setup and while a password change is pending.
func public(path string) bool {
	switch path {
	case "/manifest.webmanifest", "/sw.js", "/offline", "/favicon.ico", "/apple-touch-icon.png",
		"/apple-touch-icon-precomposed.png", "/healthz":
		return true
	}
	return strings.HasPrefix(path, "/static/")
}

// gate sends everyone to setup until it's done, and people with a temporary
// password to choose their own.
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if public(p) {
			next.ServeHTTP(w, r)
			return
		}
		if !s.setupDone.Load() && p != "/setup" {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		ri := current(r)
		if ri.sess != nil && ri.sess.MFAPending {
			if p != "/login/verify" && p != "/logout" {
				http.Redirect(w, r, "/login/verify", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if u := ri.user; u != nil && u.MustChangePassword && p != "/password" && p != "/logout" {
			http.Redirect(w, r, "/password", http.StatusSeeOther)
			return
		}
		if u := ri.user; u != nil && !u.TOTPEnabled && !strings.HasPrefix(p, "/account/two-step") && p != "/logout" &&
			s.mfaRequired(u) {
			http.Redirect(w, r, "/account/two-step", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// signedIn requires a signed-in person, and a valid CSRF token on POSTs.
func (s *Server) signedIn(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ri := current(r)
		if ri.user == nil || (ri.sess.MFAPending && r.URL.Path != "/logout") {
			if r.Method == http.MethodGet {
				next := r.URL.RequestURI()
				target := "/login"
				if next != "/" {
					target += "?next=" + url.QueryEscape(next)
				}
				http.Redirect(w, r, target, http.StatusSeeOther)
				return
			}
			s.renderError(w, r, http.StatusUnauthorized, "Please sign in", "You were signed out. Sign in and try again.")
			return
		}
		if r.Method == http.MethodPost {
			got := r.PostFormValue("csrf")
			if subtle.ConstantTimeCompare([]byte(got), []byte(ri.sess.CSRF)) != 1 {
				s.renderError(w, r, http.StatusForbidden, "That form expired", "Go back, reload the page and try again.")
				return
			}
		}
		h(w, r)
	}
}

// admin requires a signed-in admin.
func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return s.signedIn(func(w http.ResponseWriter, r *http.Request) {
		if !current(r).user.IsAdmin() {
			s.renderError(w, r, http.StatusForbidden, "Admins only", "Only admins can open that page.")
			return
		}
		h(w, r)
	})
}

// safeNext returns next if it's a path on this site, otherwise "/".
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, `/\`) {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return next
}

func waitMessage(d time.Duration) string {
	m := int(d.Minutes() + 0.999)
	if m <= 1 {
		return "Too many tries. Wait a minute and try again."
	}
	return fmt.Sprintf("Too many tries. Wait %d minutes and try again.", m)
}
