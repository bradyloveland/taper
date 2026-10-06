package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/taper/internal/release"
	"github.com/bradyloveland/taper/internal/store"
	"github.com/bradyloveland/taper/internal/version"
)

// releaseKit makes signed release archives that a test server trusts.
type releaseKit struct {
	priv ed25519.PrivateKey
	key  release.Key
}

func newReleaseKit() *releaseKit {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	return &releaseKit{priv: priv, key: release.Key{ID: "test", Pub: pub}}
}

func (k *releaseKit) archive(t *testing.T, ver string, signed bool) []byte {
	t.Helper()
	files := map[string]string{"taper": "taper " + ver, "install.sh": "#!/bin/sh\n",
		"CHANGELOG.md": "## [" + ver + "] - 2026-10-06\n\n### Added\n- **Shiny** new things.\n"}
	m := &release.Manifest{Version: ver, Arch: runtime.GOARCH, Files: map[string]string{}}
	for n, b := range files {
		m.Files[n] = release.Hash([]byte(b))
	}
	raw := m.Encode()
	files["MANIFEST"] = string(raw)
	if signed {
		files["MANIFEST.sig"] = string(release.Sign("test", k.priv, raw))
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	top := fmt.Sprintf("taper-%s-linux-%s/", ver, runtime.GOARCH)
	tw.WriteHeader(&tar.Header{Name: top, Typeflag: tar.TypeDir, Mode: 0o755})
	for n, b := range files {
		tw.WriteHeader(&tar.Header{Name: top + n, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(b))})
		tw.Write([]byte(b))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// updateEnv is a set-up server that can update itself, with a fake GitHub.
func updateEnv(t *testing.T) (*env, *releaseKit) {
	t.Helper()
	kit := newReleaseKit()
	newer := "99.0.0"
	file := kit.archive(t, newer, true)
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases":
			fmt.Fprintf(w, `[{"tag_name":"v%[2]s","body":"## Added\n- **Shiny** new things.","html_url":"https://github.com/x/y/releases/v%[2]s",
			  "assets":[{"name":"taper-%[2]s-linux-%[3]s.tar.gz","browser_download_url":"%[1]s/file"}]}]`, gh.URL, newer, runtime.GOARCH)
		case "/file":
			w.Write(file)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gh.Close)
	e := newEnvReadyWith(t, func(o *Options) {
		o.UpdateKeys = []release.Key{kit.key}
		o.UpdateAPI = gh.URL
		o.Supervised = func() bool { return true }
		o.Executable = filepath.Join(o.Config.AppDir, "taper")
	})
	if err := os.WriteFile(filepath.Join(e.cfg.AppDir, "taper"), []byte("taper old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return e, kit
}

func (b *browser) upload(path, field, name string, data []byte) *resp {
	b.e.t.Helper()
	token := b.csrf()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("csrf", token)
	fw, _ := mw.CreateFormFile(field, name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("POST", b.e.ts.URL+path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return b.do(req)
}

func TestUpdatesPageAccess(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	expect(t, e.signedIn("mia", "mentor-password").get("/admin/updates"), http.StatusForbidden)
	b := e.signedIn("admin", "admin-password")
	// Outside systemd (as in tests and `make dev`), updating isn't possible.
	expect(t, b.get("/admin/updates"), http.StatusOK, "This server runs Taper "+version.Version, "isn't running as the taper system service", "Check now")
	expect(t, b.postForm("/admin/updates/install", nil), http.StatusUnprocessableEntity, "isn't running as the taper system service")
	expect(t, b.postForm("/admin/updates/rollback", nil), http.StatusUnprocessableEntity, "no earlier version")
	r := b.get("/healthz")
	if strings.TrimSpace(r.Body) != "ok "+version.Version {
		t.Fatalf("healthz = %q", r.Body)
	}
}

func TestUpdateFromGitHub(t *testing.T) {
	e, _ := updateEnv(t)
	b := e.signedIn("admin", "admin-password")
	page := b.get("/admin/updates")
	expect(t, page, http.StatusOK, "hasn't checked GitHub")
	if strings.Contains(page.Body, "system service") {
		t.Fatalf("updating should be possible:\n%s", page.Body)
	}
	expectRedirect(t, b.postForm("/admin/updates/check", nil), "/admin/updates")
	expect(t, b.get("/admin/updates"), http.StatusOK, "Taper 99.0.0 is available", "<strong>Shiny</strong>", "Download 99.0.0")
	// Admins see it on the home page too.
	expect(t, b.get("/"), http.StatusOK, "Taper 99.0.0 is available")

	expect(t, b.postForm("/admin/updates/download", url.Values{"version": {"98.0.0"}}), http.StatusBadGateway, "Check for updates again")
	expectRedirect(t, b.postForm("/admin/updates/download", url.Values{"version": {"99.0.0"}}), "/admin/updates")
	expect(t, b.get("/admin/updates"), http.StatusOK, "Ready to install: Taper 99.0.0", "Install 99.0.0")

	r := b.postForm("/admin/updates/install", nil)
	expect(t, r, http.StatusOK, "Installing Taper 99.0.0", `data-wait-version="99.0.0"`)
	select {
	case <-e.srv.Restarting():
	case <-time.After(3 * time.Second):
		t.Fatal("the server should restart after installing")
	}
	if e.srv.RollbackRequested() != "" {
		t.Fatal("an install isn't a rollback")
	}
	got, _ := os.ReadFile(filepath.Join(e.cfg.AppDir, "taper"))
	prev, _ := os.ReadFile(filepath.Join(e.cfg.AppDir, "taper.prev"))
	if string(got) != "taper 99.0.0" || string(prev) != "taper old" {
		t.Fatalf("files not swapped: %q %q", got, prev)
	}
	backups, _ := filepath.Glob(filepath.Join(e.cfg.DataDir, "backups", "taper-*.db"))
	if len(backups) != 1 {
		t.Fatalf("backups: %v", backups)
	}
}

func TestUpdateUpload(t *testing.T) {
	e, kit := updateEnv(t)
	b := e.signedIn("admin", "admin-password")
	expect(t, b.upload("/admin/updates/upload", "release", "x.tar.gz", kit.archive(t, "99.1.0", false)), http.StatusUnprocessableEntity, "isn't signed")
	expect(t, b.upload("/admin/updates/upload", "release", "x.tar.gz", []byte("not a release")), http.StatusUnprocessableEntity, "isn't a Taper release")
	other := newReleaseKit()
	expect(t, b.upload("/admin/updates/upload", "release", "x.tar.gz", other.archive(t, "99.1.0", true)), http.StatusUnprocessableEntity, "isn't signed")
	expect(t, b.upload("/admin/updates/upload", "wrong", "x.tar.gz", []byte("x")), http.StatusUnprocessableEntity, "Choose a release file")

	expectRedirect(t, b.upload("/admin/updates/upload", "release", "x.tar.gz", kit.archive(t, "99.1.0", true)), "/admin/updates")
	expect(t, b.get("/admin/updates"), http.StatusOK, "Ready to install: Taper 99.1.0", "<strong>Shiny</strong>")
	expectRedirect(t, b.postForm("/admin/updates/discard", nil), "/admin/updates")
	expect(t, b.get("/admin/updates"), http.StatusOK, "Install from a file")

	// An older release can be staged but not installed.
	old := kit.archive(t, "0.0.1", true)
	expectRedirect(t, b.upload("/admin/updates/upload", "release", "x.tar.gz", old), "/admin/updates")
	page := b.get("/admin/updates")
	expect(t, page, http.StatusOK, "isn't newer than the running version")
	if strings.Contains(page.Body, "Install 0.0.1") {
		t.Fatal("an older release shouldn't offer Install")
	}
	expect(t, b.postForm("/admin/updates/install", nil), http.StatusUnprocessableEntity, "isn't newer")

	// Daily checks can be turned off.
	expectRedirect(t, b.postForm("/admin/updates/settings", url.Values{}), "/admin/updates")
	if e.srv.checkDaily() {
		t.Fatal("daily checks should be off")
	}
	expectRedirect(t, b.postForm("/admin/updates/settings", url.Values{"check_daily": {"1"}}), "/admin/updates")
	if !e.srv.checkDaily() {
		t.Fatal("daily checks should be on")
	}
}

func TestBackupDownload(t *testing.T) {
	e := newEnvReady(t)
	e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	expect(t, e.signedIn("mia", "mentor-password").get("/admin/backup"), http.StatusForbidden)
	b := e.signedIn("admin", "admin-password")
	req, _ := http.NewRequest("GET", e.ts.URL+"/admin/backup", nil)
	res, err := b.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.HasPrefix(string(data), "SQLite format 3") ||
		!strings.Contains(res.Header.Get("Content-Disposition"), `attachment; filename="taper-`) {
		t.Fatalf("backup: %d %q %q", res.StatusCode, res.Header.Get("Content-Disposition"), data[:min(len(data), 20)])
	}
	left, _ := filepath.Glob(filepath.Join(e.cfg.DataDir, "backups", "download-*"))
	if len(left) != 0 {
		t.Fatalf("temporary backup left behind: %v", left)
	}
}
