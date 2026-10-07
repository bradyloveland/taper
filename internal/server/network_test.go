package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/taper/internal/config"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func answers(port int) bool {
	c := &http.Client{Timeout: time.Second}
	res, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// serving starts the env's server listening on a real port.
func serving(t *testing.T, e *env, timeout time.Duration) int {
	t.Helper()
	port := freePort(t)
	e.cfg.Port, e.cfg.Bind, e.cfg.ConfirmTimeout = port, "127.0.0.1", timeout
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.srv.Serve(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "the server to listen", func() bool { return answers(port) })
	return port
}

func netForm(port int, bind string) url.Values {
	return url.Values{"mode": {"http"}, "port": {fmt.Sprint(port)}, "bind": {bind}}
}

func savedPort(t *testing.T, e *env) string {
	vals, err := config.ReadNetwork(e.cfg.DataDir)
	if err != nil || vals == nil {
		return ""
	}
	return vals["TAPER_PORT"]
}

func TestNetworkChangeConfirmed(t *testing.T) {
	e := newEnvReady(t)
	a := serving(t, e, time.Minute)
	b := freePort(t)
	admin := e.browser()
	admin.base = fmt.Sprintf("http://127.0.0.1:%d", a)
	if r := admin.login("admin", "admin-password"); r.Status != http.StatusSeeOther {
		t.Fatalf("login: %d", r.Status)
	}
	expect(t, admin.get("/admin/network"), http.StatusOK, "Network &amp; HTTPS", "Plain HTTP on 127.0.0.1:")
	expect(t, admin.postForm("/admin/network", netForm(0, "")), http.StatusUnprocessableEntity, "port must be a number")
	expect(t, admin.postForm("/admin/network", url.Values{"mode": {"https"}, "domain": {"localhost"}}), http.StatusUnprocessableEntity, "public name")

	expectRedirect(t, admin.postForm("/admin/network", netForm(b, "127.0.0.1")), "/admin/network")
	if !answers(a) || !answers(b) {
		t.Fatal("old and new addresses should both answer while trying")
	}
	expect(t, admin.get("/admin/network"), http.StatusOK, "Trying new settings", fmt.Sprintf("http://127.0.0.1:%d/admin/network", b))
	expect(t, admin.postForm("/admin/network", netForm(freePort(t), "127.0.0.1")), http.StatusUnprocessableEntity, "already waiting")
	// Keeping only works from the new address.
	expect(t, admin.postForm("/admin/network/confirm", nil), http.StatusUnprocessableEntity, "Open Taper at the new address")
	if savedPort(t, e) != "" {
		t.Fatal("nothing should be saved before confirming")
	}

	viaNew := e.browser()
	viaNew.c.Jar = admin.c.Jar // the same browser, at the new address
	viaNew.base = fmt.Sprintf("http://127.0.0.1:%d", b)
	expect(t, viaNew.get("/admin/network"), http.StatusOK, "The new settings work. Keep them?")
	expectRedirect(t, viaNew.postForm("/admin/network/confirm", nil), "/admin/network")
	if savedPort(t, e) != fmt.Sprint(b) {
		t.Fatalf("saved port %q", savedPort(t, e))
	}
	eventually(t, "the old address to close", func() bool { return !answers(a) })
	expect(t, viaNew.get("/admin/network"), http.StatusOK, "The new network settings are saved", fmt.Sprintf("127.0.0.1:%d", b))
	if strings.Contains(viaNew.last, "Keep them?") {
		t.Fatal("nothing should be pending after confirming")
	}
}

func TestNetworkChangeUndone(t *testing.T) {
	e := newEnvReady(t)
	a := serving(t, e, 300*time.Millisecond)
	admin := e.browser()
	admin.base = fmt.Sprintf("http://127.0.0.1:%d", a)
	admin.login("admin", "admin-password")

	// Not confirmed in time.
	b := freePort(t)
	expectRedirect(t, admin.postForm("/admin/network", netForm(b, "127.0.0.1")), "/admin/network")
	eventually(t, "the trial to end", func() bool { return !answers(b) })
	if !answers(a) {
		t.Fatal("the old address should keep working")
	}
	expect(t, admin.get("/admin/network"), http.StatusOK, "weren't confirmed in time")

	// Cancelled from the old address.
	e.cfg.ConfirmTimeout = time.Minute
	c := freePort(t)
	expectRedirect(t, admin.postForm("/admin/network", netForm(c, "127.0.0.1")), "/admin/network")
	expect(t, admin.postForm("/admin/network/cancel", nil), http.StatusSeeOther)
	eventually(t, "the cancelled address to close", func() bool { return !answers(c) })
	expect(t, admin.get("/admin/network"), http.StatusOK, "You cancelled the network change")
	if savedPort(t, e) != "" {
		t.Fatal("nothing should be saved")
	}

	// A port another program uses is refused straight away.
	busy, _ := net.Listen("tcp", "127.0.0.1:0")
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	expect(t, admin.postForm("/admin/network", netForm(port, "127.0.0.1")), http.StatusUnprocessableEntity, "another program is already using")
}

func TestNetworkSamePortSwap(t *testing.T) {
	e := newEnvReady(t)
	a := serving(t, e, time.Minute)
	admin := e.browser()
	admin.base = fmt.Sprintf("http://127.0.0.1:%d", a)
	admin.login("admin", "admin-password")
	// Same port, every address instead of 127.0.0.1: the old listener must
	// stop before the new one can start.
	expectRedirect(t, admin.postForm("/admin/network", netForm(a, "")), "/admin/network")
	time.Sleep(1500 * time.Millisecond)
	eventually(t, "the swapped listener", func() bool { return answers(a) })
	expect(t, admin.get("/admin/network"), http.StatusOK, "The new settings work. Keep them?")
	expectRedirect(t, admin.postForm("/admin/network/confirm", nil), "/admin/network")
	vals, _ := config.ReadNetwork(e.cfg.DataDir)
	if vals["TAPER_BIND"] != "" || vals["TAPER_PORT"] != fmt.Sprint(a) {
		t.Fatalf("saved %v", vals)
	}
}

func TestNetworkSaveWithoutTrial(t *testing.T) {
	e := newEnvReady(t)
	a := serving(t, e, time.Minute)
	admin := e.browser()
	admin.base = fmt.Sprintf("http://127.0.0.1:%d", a)
	admin.login("admin", "admin-password")
	// Only the trusted proxies change, so it's saved and used at once.
	f := netForm(a, "127.0.0.1")
	f.Set("proxies", "10.0.0.0/8, 192.168.1.20")
	expectRedirect(t, admin.postForm("/admin/network", f), "/admin/network")
	expect(t, admin.get("/admin/network"), http.StatusOK, "Saved.", `value="10.0.0.0/8,192.168.1.20"`)
	if !e.srv.currentNet().Trusted("192.168.1.20:5") {
		t.Fatal("the change should apply at once")
	}
	expect(t, admin.postForm("/admin/network", url.Values{"mode": {"http"}, "port": {fmt.Sprint(a)}, "bind": {"127.0.0.1"}, "proxies": {"nope"}}),
		http.StatusUnprocessableEntity, "Trusted proxies must be")

	// The public address.
	expect(t, admin.postForm("/admin/network/public", url.Values{"public_url": {"ftp://x"}}), http.StatusUnprocessableEntity, "should look like")
	expectRedirect(t, admin.postForm("/admin/network/public", url.Values{"public_url": {"https://learn.example.org/"}}), "/admin/network")
	if e.srv.publicURL() != "https://learn.example.org" {
		t.Fatalf("public url %q", e.srv.publicURL())
	}
	req, _ := http.NewRequest("GET", "http://anything/", nil)
	if e.srv.baseURL(req) != "https://learn.example.org" {
		t.Fatal("the public address should be used in links")
	}
	// Mentors can't see any of this.
	if _, err := os.Stat(filepath.Join(e.cfg.DataDir, config.NetworkFile)); err != nil {
		t.Fatal("settings should be saved")
	}
}
