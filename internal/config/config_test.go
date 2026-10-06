package config

import "testing"

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
