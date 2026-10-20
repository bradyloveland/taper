// Command upgradetest checks that a school's data survives upgrading from
// every published release to the build being tested.
//
// It downloads each release (checking its checksum), and:
//
//   - for each release on its own: runs it on a fresh data folder, fills it in
//     through the web pages with what that version could do (people, classes,
//     events, assignments and feedback, chat, parents), stops it, starts the
//     new build on the same folder, and checks everything is there and works;
//   - in a chain: starts with the oldest release and upgrades through every
//     later one in turn, adding what each one added, then to the new build.
//
// Usage (from the repository, after make build):
//
//	go run ./tools/upgradetest -new bin/taper
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const repo = "bradyloveland/taper"

var (
	newBin   = flag.String("new", "bin/taper", "the build to upgrade to")
	versions = flag.String("versions", "", "releases to test, comma-separated (default: every published one)")
	cache    = flag.String("cache", filepath.Join(os.TempDir(), "taper-releases"), "where downloaded releases are kept")
	failed   = false
)

func main() {
	log.SetFlags(0)
	flag.Parse()
	newAbs, err := filepath.Abs(*newBin)
	check(err)
	if _, err := os.Stat(newAbs); err != nil {
		log.Fatalf("%s isn't there: run make build first", *newBin)
	}
	list := releases()
	log.Printf("Releases: %s", strings.Join(list, ", "))
	bins := map[string]string{}
	for _, v := range list {
		bins[v] = download(v)
	}
	schema := latestSchema()

	for _, v := range list {
		log.Printf("\n== %s, then the new build", v)
		dir := mustTemp("upgrade-" + v)
		s := seed(&state{}, start(bins[v], v, dir), v)
		s.srv.stop()
		verify(s, start(newAbs, "", dir), schema)
		os.RemoveAll(dir)
	}

	log.Printf("\n== Every release in turn (%s), then the new build", strings.Join(list, " → "))
	dir := mustTemp("upgrade-chain")
	s := &state{}
	for _, v := range list {
		s = seed(s, start(bins[v], v, dir), v)
		s.srv.stop()
	}
	verify(s, start(newAbs, "", dir), schema)
	os.RemoveAll(dir)

	if failed {
		log.Fatal("\nUpgrade test FAILED")
	}
	log.Print("\nEvery upgrade kept the school's data.")
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func mustTemp(name string) string {
	d, err := os.MkdirTemp("", "taper-"+name+"-")
	check(err)
	return d
}

func fail(format string, a ...any) {
	failed = true
	log.Printf("  FAIL: "+format, a...)
}

// ------------------------------------------------------------- releases

// releases lists the published versions, oldest first.
func releases() []string {
	if *versions != "" {
		return strings.Split(*versions, ",")
	}
	res, err := http.Get("https://api.github.com/repos/" + repo + "/releases?per_page=100")
	check(err)
	defer res.Body.Close()
	var rel []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	check(json.NewDecoder(res.Body).Decode(&rel))
	var out []string
	for _, r := range rel {
		if !r.Draft && !r.Prerelease && strings.HasPrefix(r.Tag, "v") {
			out = append(out, strings.TrimPrefix(r.Tag, "v"))
		}
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	if len(out) == 0 {
		log.Fatal("no releases found")
	}
	return out
}

func less(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// atLeast reports whether version v (a release; "" is the new build) is
// min or later.
func atLeast(v, min string) bool { return v == "" || !less(v, min) }

// download fetches a release's program, checking it against SHA256SUMS.
func download(v string) string {
	name := fmt.Sprintf("taper-%s-linux-%s", v, runtime.GOARCH)
	bin := filepath.Join(*cache, name, "taper")
	if _, err := os.Stat(bin); err == nil {
		return bin
	}
	base := fmt.Sprintf("https://github.com/%s/releases/download/v%s/", repo, v)
	get := func(u string) []byte {
		res, err := http.Get(u)
		check(err)
		defer res.Body.Close()
		if res.StatusCode != 200 {
			log.Fatalf("%s: %s", u, res.Status)
		}
		b, err := io.ReadAll(res.Body)
		check(err)
		return b
	}
	archive := get(base + name + ".tar.gz")
	sums := string(get(base + "SHA256SUMS"))
	sum := sha256.Sum256(archive)
	if !strings.Contains(sums, hex.EncodeToString(sum[:])+"  "+name+".tar.gz") {
		log.Fatalf("%s.tar.gz doesn't match SHA256SUMS", name)
	}
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	check(err)
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		check(err)
		if h.Name == name+"/taper" {
			check(os.MkdirAll(filepath.Dir(bin), 0o755))
			f, err := os.OpenFile(bin, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			check(err)
			_, err = io.Copy(f, tr)
			check(err)
			check(f.Close())
			log.Printf("Downloaded %s", v)
			return bin
		}
	}
	log.Fatalf("%s has no program in it", name)
	return ""
}

// latestSchema is the newest migration in this repository.
func latestSchema() int {
	names, err := filepath.Glob("internal/store/migrations/*.sql")
	check(err)
	n := 0
	for _, f := range names {
		if v, err := strconv.Atoi(strings.SplitN(filepath.Base(f), "_", 2)[0]); err == nil && v > n {
			n = v
		}
	}
	if n == 0 {
		log.Fatal("run this from the repository: no migrations found")
	}
	return n
}

// --------------------------------------------------------------- servers

type server struct {
	cmd     *exec.Cmd
	base    string
	dir     string
	version string // "" for the new build
	out     *bytes.Buffer
}

func start(bin, version, dir string) *server {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	check(err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	s := &server{base: fmt.Sprintf("http://127.0.0.1:%d", port), dir: dir, version: version, out: &bytes.Buffer{}}
	s.cmd = exec.Command(bin, "serve")
	s.cmd.Env = append(os.Environ(), "TAPER_DATA_DIR="+dir, "TAPER_APP_DIR="+filepath.Dir(bin), "TAPER_MODE=http",
		"TAPER_BIND=127.0.0.1", "TAPER_PORT="+strconv.Itoa(port))
	s.cmd.Stdout, s.cmd.Stderr = s.out, s.out
	check(s.cmd.Start())
	name := version
	if name == "" {
		name = "the new build"
	}
	for i := 0; i < 100; i++ {
		res, err := http.Get(s.base + "/healthz")
		if err == nil {
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if strings.HasPrefix(string(b), "ok ") {
				log.Printf("  %s started (%s)", name, strings.TrimSpace(string(b)))
				return s
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Fatalf("%s didn't start:\n%s", name, s.out)
	return nil
}

func (s *server) stop() {
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
		log.Fatalf("it didn't stop:\n%s", s.out)
	}
	if strings.Contains(s.out.String(), "level=ERROR") {
		fail("errors in the log:\n%s", s.out)
	}
}

// --------------------------------------------------------------- browsing

type browser struct {
	srv  *server
	c    *http.Client
	last string
}

func (s *server) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{srv: s, c: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type page struct {
	status   int
	body     string
	location string
}

func (b *browser) do(req *http.Request) page {
	res, err := b.c.Do(req)
	check(err)
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	b.last = string(body)
	return page{res.StatusCode, string(body), res.Header.Get("Location")}
}

func (b *browser) get(path string) page {
	req, _ := http.NewRequest("GET", b.srv.base+path, nil)
	return b.do(req)
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// post sends a form from the page at from (fetched for its CSRF token).
func (b *browser) post(from, path string, form url.Values) page {
	if from != "" {
		b.get(from)
	}
	if m := csrfRE.FindStringSubmatch(b.last); m != nil {
		form.Set("csrf", strings.ReplaceAll(m[1], "&#43;", "+"))
	}
	req, _ := http.NewRequest("POST", b.srv.base+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", b.srv.base)
	return b.do(req)
}

func (b *browser) multipart(from, path string, form url.Values) page {
	b.get(from)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if m := csrfRE.FindStringSubmatch(b.last); m != nil {
		mw.WriteField("csrf", strings.ReplaceAll(m[1], "&#43;", "+"))
	}
	for k, vs := range form {
		for _, v := range vs {
			mw.WriteField(k, v)
		}
	}
	mw.Close()
	req, _ := http.NewRequest("POST", b.srv.base+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", b.srv.base)
	return b.do(req)
}

func (b *browser) login(username, password string) bool {
	r := b.post("/login", "/login", url.Values{"username": {username}, "password": {password}})
	return r.status == http.StatusSeeOther
}

// ----------------------------------------------------------------- seeding

// state is what has been put in the school, to check later.
type state struct {
	srv       *server
	passwords map[string]string
	ids       map[string]string // names to IDs, for URLs
	has       map[string]bool   // features filled in
	today     string
}

const (
	featPeople      = "people"
	featClasses     = "classes"
	featEvents      = "events"
	featAssignments = "assignments"
	featChat        = "chat"
	featParents     = "parents"
	featNotify      = "notifications"
)

func idFrom(location, prefix string) string {
	rest := strings.TrimPrefix(location, prefix)
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if _, err := strconv.Atoi(rest); err != nil {
		return ""
	}
	return rest
}

var personLinkRE = regexp.MustCompile(`href="/admin/people/(\d+)"[^>]*>([^<]+)<`)

// seed fills in what version v can do that the school doesn't have yet.
func seed(s *state, srv *server, v string) *state {
	s.srv = srv
	if s.passwords == nil {
		s.passwords, s.ids, s.has = map[string]string{}, map[string]string{}, map[string]bool{}
		s.today = time.Now().Format("2006-01-02")
		code, err := os.ReadFile(filepath.Join(srv.dir, "setup-code"))
		check(err)
		b := srv.browser()
		r := b.post("/setup", "/setup", url.Values{"code": {strings.TrimSpace(string(code))}, "school": {"Upgrade Academy"},
			"display_name": {"Ada Admin"}, "username": {"admin"}, "password": {"admin-password-1"}, "confirm": {"admin-password-1"}})
		if r.status != http.StatusSeeOther {
			log.Fatalf("setup on %s: %d\n%s", v, r.status, r.body)
		}
		s.passwords["admin"] = "admin-password-1"
	}
	admin := srv.browser()
	if !admin.login("admin", s.passwords["admin"]) {
		log.Fatalf("%s: the admin can't sign in", v)
	}
	add := func(name, username, role string) {
		r := admin.post("/admin/people/new", "/admin/people/new", url.Values{"display_name": {name}, "username": {username}, "role": {role}})
		m := regexp.MustCompile(`id="temp-password"[^>]*>([^<]+)<`).FindStringSubmatch(r.body)
		if r.status != http.StatusCreated || m == nil {
			log.Fatalf("%s: adding %s: %d", v, username, r.status)
		}
		b := srv.browser()
		pw := username + "-password-1"
		b.login(username, m[1])
		if r := b.post("/password", "/password", url.Values{"password": {pw}, "confirm": {pw}}); r.status != http.StatusSeeOther {
			log.Fatalf("%s: %s choosing a password: %d", v, username, r.status)
		}
		s.passwords[username] = pw
	}
	as := func(username string) *browser {
		b := srv.browser()
		if !b.login(username, s.passwords[username]) {
			log.Fatalf("%s: %s can't sign in", v, username)
		}
		return b
	}
	if !s.has[featPeople] {
		add("Mia Mentor", "mia", "mentor")
		add("Sam Scholar", "sam", "scholar")
		add("Zoe Scholar", "zoe", "scholar")
		for _, m := range personLinkRE.FindAllStringSubmatch(admin.get("/admin/people").body, -1) {
			s.ids[strings.TrimSpace(m[2])] = m[1]
		}
		s.has[featPeople] = true
		log.Printf("  added people")
	}
	if atLeast(v, "0.4.0") && !s.has[featClasses] {
		r := admin.post("/classes/new", "/classes/new", url.Values{"name": {"History of Liberty"}, "color": {"teal"}, "term": {"Fall"},
			"description": {"We read the founders."}})
		c := idFrom(r.location, "/classes/")
		if c == "" {
			log.Fatalf("%s: making a class: %d %s", v, r.status, r.location)
		}
		s.ids["class"] = c
		members := "/classes/" + c + "/members"
		admin.post(members, members+"/add", url.Values{"role": {"mentor"}, "user": {s.ids["Mia Mentor"]}})
		admin.post(members, members+"/add", url.Values{"role": {"scholar"}, "user": {s.ids["Sam Scholar"], s.ids["Zoe Scholar"]}})
		s.has[featClasses] = true
		log.Printf("  added a class")
	}
	if atLeast(v, "0.5.0") && !s.has[featEvents] {
		r := admin.post("/events/new?cal="+s.ids["class"], "/events/new", url.Values{"calendar": {s.ids["class"]},
			"title": {"Field trip to the archives"}, "start_date": {s.today}, "all_day": {"1"}})
		if r.status != http.StatusSeeOther {
			log.Fatalf("%s: adding an event: %d", v, r.status)
		}
		s.has[featEvents] = true
		log.Printf("  added an event")
	}
	if atLeast(v, "0.6.0") && !s.has[featAssignments] {
		mia := as("mia")
		newA := "/classes/" + s.ids["class"] + "/assignments/new"
		r := mia.multipart(newA, newA, url.Values{"title": {"The pursuit of happiness"}, "instructions": {"Write one page."},
			"due_date": {s.today}, "publish": {"now"}, "work_online": {"1"}})
		a := idFrom(r.location, "/assignments/")
		if a == "" {
			log.Fatalf("%s: setting an assignment: %d", v, r.status)
		}
		s.ids["assignment"] = a
		sam := as("sam")
		ap := "/assignments/" + a
		if r := sam.multipart(ap, ap+"/work", url.Values{"body": {"Sam's essay about happiness."}, "action": {"turn_in"}}); r.status != http.StatusSeeOther {
			log.Fatalf("%s: turning in work: %d", v, r.status)
		}
		rev := ap + "/work/" + s.ids["Sam Scholar"]
		if r := mia.post(rev, rev, url.Values{"decision": {"needs_work"}, "feedback": {"Quote the Declaration once."}}); r.status != http.StatusSeeOther {
			log.Fatalf("%s: giving feedback: %d", v, r.status)
		}
		s.has[featAssignments] = true
		log.Printf("  set an assignment; work turned in and sent back")
	}
	if atLeast(v, "0.7.0") && !s.has[featChat] {
		sam := as("sam")
		m := regexp.MustCompile(`href="/chat/(\d+)"`).FindStringSubmatch(sam.get("/classes/" + s.ids["class"]).body)
		if m == nil {
			log.Fatalf("%s: no class chat on the class page", v)
		}
		s.ids["chat"] = m[1]
		if r := sam.post("/chat/"+m[1], "/chat/"+m[1], url.Values{"body": {"Hello from before the upgrade"}}); r.status >= 400 {
			log.Fatalf("%s: posting in chat: %d", v, r.status)
		}
		s.has[featChat] = true
		log.Printf("  posted in the class chat")
	}
	if atLeast(v, "0.7.0") && !s.has[featParents] {
		add("Pam Parent", "pam", "parent")
		for _, m := range personLinkRE.FindAllStringSubmatch(admin.get("/admin/people").body, -1) {
			s.ids[strings.TrimSpace(m[2])] = m[1]
		}
		p := "/admin/people/" + s.ids["Pam Parent"]
		if r := admin.post(p, p+"/family/add", url.Values{"person": {s.ids["Sam Scholar"]}}); r.status != http.StatusSeeOther {
			log.Fatalf("%s: linking a parent: %d", v, r.status)
		}
		s.has[featParents] = true
		log.Printf("  added a parent")
	}
	if atLeast(v, "0.8.0") && !s.has[featNotify] {
		form := url.Values{"email": {"off"}}
		for _, k := range []string{"assignment", "work", "due", "calendar"} { // chat off
			form.Set("kind_"+k, "1")
		}
		if r := as("sam").post("/account", "/account/notifications", form); r.status != http.StatusSeeOther {
			log.Fatalf("%s: choosing notifications: %d", v, r.status)
		}
		s.has[featNotify] = true
		log.Printf("  chose notifications")
	}
	return s
}

// ---------------------------------------------------------------- checking

// verify checks, on the new build, that everything seeded is there.
func verify(s *state, srv *server, schema int) {
	defer srv.stop()
	b := map[string]*browser{}
	for user, pw := range s.passwords {
		b[user] = srv.browser()
		if !b[user].login(user, pw) {
			fail("%s can't sign in with their password", user)
			return
		}
	}
	want := func(who, path string, texts ...string) {
		r := b[who].get(path)
		if r.status != http.StatusOK {
			fail("%s %s: %d", who, path, r.status)
			return
		}
		for _, t := range texts {
			if !strings.Contains(r.body, t) {
				fail("%s %s doesn't show %q", who, path, t)
			}
		}
	}
	want("admin", "/admin/settings", fmt.Sprintf("schema %d", schema))
	want("admin", "/admin/people", "Mia Mentor", "Sam Scholar", "Zoe Scholar")
	for user := range s.passwords {
		want(user, "/", "Upgrade Academy")
	}
	if s.has[featClasses] {
		want("admin", "/classes/"+s.ids["class"], "History of Liberty", "Sam Scholar", "Zoe Scholar", "Mia Mentor")
		want("sam", "/classes", "History of Liberty")
	}
	if s.has[featEvents] {
		want("sam", "/calendar?view=list&date="+s.today, "Field trip to the archives")
	}
	if s.has[featAssignments] {
		a := "/assignments/" + s.ids["assignment"]
		want("sam", a, "The pursuit of happiness", "Quote the Declaration once.", "Needs another look")
		want("mia", a, "Sam Scholar")
		// And it still works: Sam turns it in again.
		r := b["sam"].multipart(a, a+"/work", url.Values{"body": {"Sam's essay, with a quotation."}, "action": {"turn_in"}})
		if r.status != http.StatusSeeOther {
			fail("turning work in again after the upgrade: %d", r.status)
		}
	}
	if s.has[featChat] {
		want("sam", "/chat/"+s.ids["chat"], "Hello from before the upgrade")
	}
	if s.has[featParents] {
		want("pam", "/", "Sam Scholar")
		if s.has[featClasses] {
			want("pam", "/classes/"+s.ids["class"], "History of Liberty")
		}
	}
	if s.has[featNotify] {
		want("sam", "/account", `name="kind_assignment" value="1" checked`)
		if strings.Contains(b["sam"].last, `name="kind_chat" value="1" checked`) {
			fail("sam's notification choices weren't kept")
		}
	}
	// Something new works too: adding a person, and every main page.
	if r := b["admin"].post("/admin/people/new", "/admin/people/new", url.Values{"display_name": {"New Person"}, "username": {"newbie"},
		"role": {"scholar"}}); r.status != http.StatusCreated {
		fail("adding someone after the upgrade: %d", r.status)
	}
	for _, p := range []string{"/", "/classes", "/calendar", "/chat", "/notifications", "/account", "/guide"} {
		if r := b["admin"].get(p); r.status != http.StatusOK {
			fail("admin %s after the upgrade: %d", p, r.status)
		}
	}
	if !failed {
		log.Printf("  the new build has everything (schema %d)", schema)
	}
}
