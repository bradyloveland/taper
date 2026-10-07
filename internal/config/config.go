// Package config reads the server's settings. The installer writes them to
// /etc/taper/taper.conf, which systemd loads as an EnvironmentFile. Network
// settings changed in the web interface are saved in network.json in the data
// folder, and take precedence; running the installer with network options
// removes that file. Everything else is in the database.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Modes for serving the web interface.
const (
	ModeHTTP  = "http"  // plain HTTP on Bind:Port
	ModeHTTPS = "https" // Let's Encrypt certificate for Domain on ports 443 and 80
	ModeProxy = "proxy" // plain HTTP behind a reverse proxy that handles HTTPS
)

// Config is the server's settings.
type Config struct {
	Mode           string
	Bind           string // address to listen on; "" means every interface
	Port           int    // HTTP port in http and proxy modes
	Domain         string // https mode: the public name, e.g. learn.example.org
	Email          string // https mode: optional contact for Let's Encrypt
	TrustedProxies []netip.Prefix
	DataDir        string // holds taper.db and certificates
	AppDir         string // the program folder
	// ConfirmTimeout is how long a network change waits to be confirmed.
	ConfirmTimeout time.Duration
}

// Defaults.
const (
	DefaultPort    = 8088
	DefaultDataDir = "/var/lib/taper"
	DefaultAppDir  = "/opt/taper"
)

// NetworkFile, in the data folder, holds network settings saved from the web
// interface, as TAPER_* names and values.
const NetworkFile = "network.json"

// NetworkKeys are the settings that can be changed in the web interface.
var NetworkKeys = []string{"TAPER_MODE", "TAPER_PORT", "TAPER_BIND", "TAPER_DOMAIN", "TAPER_EMAIL", "TAPER_TRUSTED_PROXIES"}

// FromEnv reads the configuration from TAPER_* variables only.
func FromEnv() (*Config, error) { return Parse(os.Getenv) }

// Load reads the configuration from the environment, with network settings
// saved from the web interface on top.
func Load() (*Config, error) {
	env, err := FromEnv()
	if err != nil {
		return nil, err
	}
	saved, err := ReadNetwork(env.DataDir)
	if err != nil || saved == nil {
		return env, err
	}
	c, err := WithNetwork(saved)
	if err != nil {
		return nil, fmt.Errorf("%s: %w (run: sudo taper network --reset)", filepath.Join(env.DataDir, NetworkFile), err)
	}
	return c, nil
}

// WithNetwork reads the configuration from the environment, with vals in
// place of its network settings.
func WithNetwork(vals map[string]string) (*Config, error) {
	return Parse(func(k string) string {
		for _, n := range NetworkKeys {
			if n == k {
				return vals[k]
			}
		}
		return os.Getenv(k)
	})
}

// ReadNetwork returns the saved network settings, or nil if there are none.
func ReadNetwork(dataDir string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, NetworkFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	vals := map[string]string{}
	if err := json.Unmarshal(raw, &vals); err != nil {
		return nil, fmt.Errorf("%s is damaged: %w", NetworkFile, err)
	}
	return vals, nil
}

// SaveNetwork stores network settings for the next start.
func SaveNetwork(dataDir string, vals map[string]string) error {
	out := map[string]string{}
	for _, k := range NetworkKeys {
		out[k] = vals[k]
	}
	raw, _ := json.MarshalIndent(out, "", "  ")
	path := filepath.Join(dataDir, NetworkFile)
	if err := os.WriteFile(path+".tmp", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// ResetNetwork removes settings saved from the web interface, so the
// installer's settings apply again.
func ResetNetwork(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, NetworkFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// NetworkValues returns the network settings as TAPER_* names and values.
func (c *Config) NetworkValues() map[string]string {
	var proxies []string
	for _, p := range c.TrustedProxies {
		if p.IsSingleIP() {
			proxies = append(proxies, p.Addr().String())
		} else {
			proxies = append(proxies, p.String())
		}
	}
	return map[string]string{
		"TAPER_MODE": c.Mode, "TAPER_PORT": strconv.Itoa(c.Port), "TAPER_BIND": c.Bind,
		"TAPER_DOMAIN": c.Domain, "TAPER_EMAIL": c.Email, "TAPER_TRUSTED_PROXIES": strings.Join(proxies, ","),
	}
}

// SameListeners reports whether c and o listen on the same addresses in the
// same way, so a change between them doesn't need confirming.
func (c *Config) SameListeners(o *Config) bool {
	if c.Mode != o.Mode || c.Bind != o.Bind {
		return false
	}
	if c.Mode == ModeHTTPS {
		return c.Domain == o.Domain
	}
	return c.Port == o.Port
}

// Ports returns the TCP ports c listens on.
func (c *Config) Ports() []int {
	if c.Mode == ModeHTTPS {
		return []int{443, 80}
	}
	return []int{c.Port}
}

// Parse reads the configuration using getenv.
func Parse(getenv func(string) string) (*Config, error) {
	c := &Config{
		Mode:    strings.ToLower(strings.TrimSpace(getenv("TAPER_MODE"))),
		Bind:    strings.TrimSpace(getenv("TAPER_BIND")),
		Port:    DefaultPort,
		Domain:  strings.ToLower(strings.TrimSpace(getenv("TAPER_DOMAIN"))),
		Email:   strings.TrimSpace(getenv("TAPER_EMAIL")),
		DataDir: strings.TrimSpace(getenv("TAPER_DATA_DIR")),
		AppDir:  strings.TrimSpace(getenv("TAPER_APP_DIR")),
	}
	if c.Mode == "" {
		c.Mode = ModeHTTP
	}
	if c.DataDir == "" {
		c.DataDir = DefaultDataDir
	}
	if c.AppDir == "" {
		c.AppDir = DefaultAppDir
	}
	c.ConfirmTimeout = 3 * time.Minute
	if v := strings.TrimSpace(getenv("TAPER_CONFIRM_SECONDS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 5 || n > 3600 {
			return nil, fmt.Errorf("TAPER_CONFIRM_SECONDS must be from 5 to 3600, not %q", v)
		}
		c.ConfirmTimeout = time.Duration(n) * time.Second
	}
	if p := strings.TrimSpace(getenv("TAPER_PORT")); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("TAPER_PORT must be a port number from 1 to 65535, not %q", p)
		}
		c.Port = n
	}
	if c.Bind != "" && net.ParseIP(c.Bind) == nil {
		return nil, fmt.Errorf("TAPER_BIND must be an IP address, not %q", c.Bind)
	}
	switch c.Mode {
	case ModeHTTP:
	case ModeHTTPS:
		if c.Domain == "" || strings.ContainsAny(c.Domain, "/: ") || !strings.Contains(c.Domain, ".") {
			return nil, fmt.Errorf("https mode needs TAPER_DOMAIN set to the server's public name, like learn.example.org")
		}
	case ModeProxy:
		// The installer sets TAPER_BIND: 127.0.0.1 for a proxy on the same
		// machine, empty (every interface) for one elsewhere.
	default:
		return nil, fmt.Errorf("TAPER_MODE must be http, https or proxy, not %q", c.Mode)
	}
	proxies := getenv("TAPER_TRUSTED_PROXIES")
	if c.Mode == ModeProxy && strings.TrimSpace(proxies) == "" {
		proxies = "127.0.0.1,::1"
	}
	for _, f := range strings.FieldsFunc(proxies, func(r rune) bool { return r == ',' || r == ' ' }) {
		p, err := parsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf("TAPER_TRUSTED_PROXIES: %q isn't an IP address or range", f)
		}
		c.TrustedProxies = append(c.TrustedProxies, p)
	}
	return c, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

// Trusted reports whether a connection from addr (host:port or host) is a
// trusted reverse proxy, whose X-Forwarded-* headers are believed.
func (c *Config) Trusted(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range c.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Listen returns the address the HTTP server listens on in http and proxy modes.
func (c *Config) Listen() string { return net.JoinHostPort(c.Bind, strconv.Itoa(c.Port)) }

// Describe is a one-line summary for the admin pages and logs.
func (c *Config) Describe() string {
	switch c.Mode {
	case ModeHTTPS:
		return "HTTPS with a Let's Encrypt certificate for " + c.Domain
	case ModeProxy:
		return "Behind a reverse proxy, listening on " + c.Listen()
	default:
		return "Plain HTTP on " + c.Listen()
	}
}
