package server

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bradyloveland/taper/internal/config"
)

// settingPublicURL is the address people use to reach Taper, for links in
// email and calendars. Empty means work it out from each request.
const settingPublicURL = "public_url"

func (s *Server) publicURL() string {
	var u string
	_, _ = s.store.GetSetting(settingPublicURL, &u)
	return u
}

// cleanPublicURL checks an address like https://learn.example.org.
func cleanPublicURL(v string) (string, string) {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if v == "" {
		return "", ""
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", "The public address should look like https://learn.yourschool.org."
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath(), ""
}

type networkData struct {
	Current    *config.Config
	Form       map[string]string
	Error      string
	Notice     string
	Pending    *config.Config
	Expires    time.Time
	ViaPending bool
	Swapping   bool
	TryURL     string
	PublicURL  string
	PublicErr  string
	Guess      string // the address Taper works out when there's no public address
}

func formFrom(c *config.Config) map[string]string {
	v := c.NetworkValues()
	return map[string]string{"mode": v["TAPER_MODE"], "port": v["TAPER_PORT"], "bind": v["TAPER_BIND"],
		"domain": v["TAPER_DOMAIN"], "email": v["TAPER_EMAIL"], "proxies": v["TAPER_TRUSTED_PROXIES"]}
}

// tryURL is where to open Taper with new settings, to confirm them.
func tryURL(c *config.Config, r *http.Request) string {
	if c.Mode == config.ModeHTTPS {
		return "https://" + c.Domain + "/admin/network"
	}
	host := c.Bind
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
	}
	return fmt.Sprintf("http://%s/admin/network", net.JoinHostPort(host, fmt.Sprint(c.Port)))
}

func (s *Server) renderNetwork(w http.ResponseWriter, r *http.Request, status int, d networkData) {
	d.Current = s.currentNet()
	if d.Current == nil || viaPending(r) {
		d.Current = s.savedNet()
	}
	if d.Form == nil {
		d.Form = formFrom(d.Current)
	}
	s.netMu.Lock()
	if p := s.netPend; p != nil {
		d.Pending, d.Expires, d.Swapping = p.cfg, p.expires, p.stoppedOld
		d.TryURL = tryURL(p.cfg, r)
	}
	if d.Notice == "" {
		d.Notice = s.netNotice
	}
	s.netMu.Unlock()
	d.ViaPending = viaPending(r)
	d.PublicURL = s.publicURL()
	cfg := s.netcfg(r)
	if cfg.Mode == config.ModeHTTPS {
		d.Guess = "https://" + cfg.Domain
	} else {
		scheme := "http"
		if s.secure(r) {
			scheme = "https"
		}
		d.Guess = scheme + "://" + r.Host
	}
	s.render(w, r, status, "network", "Network & HTTPS", "network", d)
}

func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request) {
	s.renderNetwork(w, r, http.StatusOK, networkData{})
}

func (s *Server) handleNetworkSave(w http.ResponseWriter, r *http.Request) {
	form := formValues(r, "mode", "port", "bind", "domain", "email", "proxies")
	fail := func(msg string) {
		s.renderNetwork(w, r, http.StatusUnprocessableEntity, networkData{Form: form, Error: msg})
	}
	if form["mode"] != config.ModeHTTPS {
		form["domain"], form["email"] = "", ""
	}
	cfg, err := config.WithNetwork(map[string]string{"TAPER_MODE": form["mode"], "TAPER_PORT": form["port"],
		"TAPER_BIND": form["bind"], "TAPER_DOMAIN": form["domain"], "TAPER_EMAIL": form["email"],
		"TAPER_TRUSTED_PROXIES": form["proxies"]})
	if err != nil {
		fail(networkMessage(err))
		return
	}
	cfg.DataDir, cfg.AppDir, cfg.ConfirmTimeout = s.cfg.DataDir, s.cfg.AppDir, s.cfg.ConfirmTimeout
	if form["port"] == "" && cfg.Mode != config.ModeHTTPS {
		fail("Enter a port number.")
		return
	}
	who := current(r).user.Username
	if cfg.SameListeners(s.currentNet()) {
		if err := s.applyNetwork(cfg); err != nil {
			s.serverError(w, r, "saving network settings", err)
			return
		}
		slog.Info("network settings saved", "by", who)
		s.redirect(w, r, "/admin/network", "Saved.")
		return
	}
	if _, err := s.tryNetwork(cfg); err != nil {
		fail("Those settings can't be used: " + err.Error() + ".")
		return
	}
	slog.Info("network settings being tried", "by", who, "address", cfg.Describe())
	http.Redirect(w, r, "/admin/network", http.StatusSeeOther)
}

// networkMessage makes a configuration error readable in the form, which
// has labels rather than TAPER_* names.
func networkMessage(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "TAPER_PORT"):
		return "The port must be a number from 1 to 65535."
	case strings.Contains(msg, "TAPER_BIND"):
		return "The listen address must be an IP address of this server, or empty for every address."
	case strings.Contains(msg, "TAPER_DOMAIN"):
		return "Let's Encrypt needs the server's public name, like learn.yourschool.org."
	case strings.Contains(msg, "TAPER_TRUSTED_PROXIES"):
		return "Trusted proxies must be IP addresses or ranges (like 192.168.1.20 or 10.0.0.0/24), separated by commas."
	case strings.Contains(msg, "TAPER_MODE"):
		return "Choose how Taper is reached."
	}
	return capitalize(msg) + "."
}

func (s *Server) handleNetworkConfirm(w http.ResponseWriter, r *http.Request) {
	if !viaPending(r) {
		s.renderNetwork(w, r, http.StatusUnprocessableEntity, networkData{
			Error: "Open Taper at the new address to keep the new settings. That shows they work."})
		return
	}
	if err := s.confirmNetwork(); err != nil {
		s.renderNetwork(w, r, http.StatusUnprocessableEntity, networkData{Error: capitalize(err.Error()) + "."})
		return
	}
	slog.Info("network settings kept", "by", current(r).user.Username)
	s.redirect(w, r, "/admin/network", "The new network settings are saved. The old address no longer works.")
}

func (s *Server) handleNetworkCancel(w http.ResponseWriter, r *http.Request) {
	s.revertNetwork("You cancelled the network change, so the earlier settings are back.")
	if viaPending(r) {
		// This address is closing; there's nowhere to send the browser here.
		s.renderError(w, r, http.StatusOK, "Change cancelled", "The earlier network settings are back. Use the old address again.")
		return
	}
	http.Redirect(w, r, "/admin/network", http.StatusSeeOther)
}

func (s *Server) handlePublicURLSave(w http.ResponseWriter, r *http.Request) {
	u, msg := cleanPublicURL(r.PostFormValue("public_url"))
	if msg != "" {
		s.renderNetwork(w, r, http.StatusUnprocessableEntity, networkData{PublicErr: msg})
		return
	}
	if err := s.store.SetSetting(settingPublicURL, u); err != nil {
		s.serverError(w, r, "saving public address", err)
		return
	}
	s.redirect(w, r, "/admin/network", "Public address saved.")
}
