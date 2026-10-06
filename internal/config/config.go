// Package config reads the server's settings from the environment. The
// installer writes them to /etc/taper/taper.conf, which systemd loads as an
// EnvironmentFile. Everything else is stored in the database and changed in
// the web interface.
package config

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
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
}

// Defaults.
const (
	DefaultPort    = 8088
	DefaultDataDir = "/var/lib/taper"
	DefaultAppDir  = "/opt/taper"
)

// FromEnv reads the configuration from TAPER_* variables.
func FromEnv() (*Config, error) { return Parse(os.Getenv) }

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
