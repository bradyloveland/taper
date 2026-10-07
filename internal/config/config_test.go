package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := Parse(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != ModeHTTP || c.Port != DefaultPort || c.DataDir != DefaultDataDir || c.Bind != "" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.Listen() != ":8088" {
		t.Fatalf("listen = %q", c.Listen())
	}
	if c.Trusted("127.0.0.1:5000") {
		t.Fatal("http mode shouldn't trust anyone by default")
	}
}

func TestProxyMode(t *testing.T) {
	c, err := Parse(env(map[string]string{"TAPER_MODE": "proxy", "TAPER_BIND": "127.0.0.1"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen() != "127.0.0.1:8088" {
		t.Fatalf("listen = %q", c.Listen())
	}
	// An empty TAPER_BIND (a proxy on another machine) means every interface.
	if c2, _ := Parse(env(map[string]string{"TAPER_MODE": "proxy", "TAPER_BIND": ""})); c2.Bind != "" {
		t.Fatalf("empty bind became %q", c2.Bind)
	}
	for addr, want := range map[string]bool{"127.0.0.1:1": true, "[::1]:2": true, "10.0.0.5:3": false, "garbage": false} {
		if got := c.Trusted(addr); got != want {
			t.Errorf("Trusted(%q) = %v", addr, got)
		}
	}
	c, err = Parse(env(map[string]string{"TAPER_MODE": "proxy", "TAPER_BIND": "0.0.0.0", "TAPER_TRUSTED_PROXIES": "10.0.0.0/24, 192.168.1.9"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Trusted("10.0.0.77:1") || !c.Trusted("192.168.1.9:1") || c.Trusted("127.0.0.1:1") || c.Trusted("[::ffff:192.168.1.10]:1") {
		t.Fatalf("trusted proxies wrong: %v", c.TrustedProxies)
	}
	if !c.Trusted("[::ffff:192.168.1.9]:1") {
		t.Fatal("IPv4-mapped address should match")
	}
}

func TestErrors(t *testing.T) {
	for _, m := range []map[string]string{
		{"TAPER_MODE": "ftp"},
		{"TAPER_PORT": "99999"},
		{"TAPER_PORT": "abc"},
		{"TAPER_BIND": "not-an-ip"},
		{"TAPER_MODE": "https"},
		{"TAPER_MODE": "https", "TAPER_DOMAIN": "localhost"},
		{"TAPER_TRUSTED_PROXIES": "nope"},
	} {
		if _, err := Parse(env(m)); err == nil {
			t.Errorf("Parse(%v) should fail", m)
		}
	}
	c, err := Parse(env(map[string]string{"TAPER_MODE": "HTTPS", "TAPER_DOMAIN": "Learn.Example.org"}))
	if err != nil || c.Domain != "learn.example.org" || c.Mode != ModeHTTPS {
		t.Fatalf("https: %+v %v", c, err)
	}
}

func TestSavedNetwork(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TAPER_DATA_DIR", dir)
	t.Setenv("TAPER_MODE", "http")
	t.Setenv("TAPER_PORT", "8088")
	c, err := Load()
	if err != nil || c.Port != 8088 {
		t.Fatalf("env only: %+v %v", c, err)
	}
	vals := map[string]string{"TAPER_MODE": "proxy", "TAPER_PORT": "9000", "TAPER_BIND": "127.0.0.1",
		"TAPER_TRUSTED_PROXIES": "10.0.0.0/8,192.168.1.2"}
	if err := SaveNetwork(dir, vals); err != nil {
		t.Fatal(err)
	}
	c, err = Load()
	if err != nil || c.Mode != ModeProxy || c.Port != 9000 || c.Bind != "127.0.0.1" || !c.Trusted("10.1.2.3:4") || c.DataDir != dir {
		t.Fatalf("saved: %+v %v", c, err)
	}
	if got := c.NetworkValues()["TAPER_TRUSTED_PROXIES"]; got != "10.0.0.0/8,192.168.1.2" {
		t.Fatalf("values: %q", got)
	}
	if c.ConfirmTimeout.Minutes() != 3 {
		t.Fatalf("confirm timeout %v", c.ConfirmTimeout)
	}
	// Bad saved settings are reported with how to fix them.
	SaveNetwork(dir, map[string]string{"TAPER_MODE": "nope"})
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "taper network --reset") {
		t.Fatalf("bad saved settings: %v", err)
	}
	if err := ResetNetwork(dir); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load(); c.Mode != ModeHTTP {
		t.Fatal("reset should go back to the environment")
	}
	if err := ResetNetwork(dir); err != nil {
		t.Fatal("resetting twice is fine")
	}
}

func TestSameListeners(t *testing.T) {
	a, _ := Parse(env(map[string]string{"TAPER_PORT": "8088"}))
	b, _ := Parse(env(map[string]string{"TAPER_PORT": "8088", "TAPER_TRUSTED_PROXIES": "10.0.0.1"}))
	c, _ := Parse(env(map[string]string{"TAPER_PORT": "9000"}))
	d, _ := Parse(env(map[string]string{"TAPER_MODE": "https", "TAPER_DOMAIN": "a.example.org"}))
	e, _ := Parse(env(map[string]string{"TAPER_MODE": "https", "TAPER_DOMAIN": "a.example.org", "TAPER_PORT": "1"}))
	if !a.SameListeners(b) || a.SameListeners(c) || a.SameListeners(d) || !d.SameListeners(e) {
		t.Fatal("SameListeners")
	}
	if len(d.Ports()) != 2 || c.Ports()[0] != 9000 {
		t.Fatal("Ports")
	}
	if _, err := Parse(env(map[string]string{"TAPER_CONFIRM_SECONDS": "2"})); err == nil {
		t.Fatal("tiny confirm timeout accepted")
	}
}
