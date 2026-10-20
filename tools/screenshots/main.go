// Command screenshots takes the pictures in the user guide (docs/guide/images).
//
// It builds a made-up school in a throwaway database, runs Taper on it, and
// has headless Firefox photograph pages as different people, at desktop and
// phone sizes. ImageMagick makes the files small. Run it with `make
// screenshots` whenever pages change; it needs Firefox and ImageMagick, like
// `make icons`.
//
// Everyone in the demo school is made up.
package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/cal"
	"github.com/bradyloveland/taper/internal/config"
	"github.com/bradyloveland/taper/internal/server"
	"github.com/bradyloveland/taper/internal/store"
)

// shot is one picture.
type shot struct {
	File   string // under docs/guide/images
	As     string // username
	Path   string
	Width  int
	Height int
	Menu   bool // with the account menu open
}

const (
	desktopW = 1280
	phoneW   = 390
)

func main() {
	log.SetFlags(0)
	out := "docs/guide/images"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	for _, tool := range []string{"firefox", "convert"} {
		if _, err := exec.LookPath(tool); err != nil {
			log.Fatalf("%s is needed: install Firefox and ImageMagick", tool)
		}
	}
	dir, err := os.MkdirTemp("", "taper-screenshots-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	st, err := store.Open(filepath.Join(dir, "taper.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	ids := seed(st)

	cfg := &config.Config{Mode: config.ModeHTTP, Port: 8088, DataDir: dir, AppDir: dir, Label: ""}
	srv, err := server.New(server.Options{Config: cfg, Store: st, Supervised: func() bool { return false }, UpdateAPI: "http://127.0.0.1:1"})
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	go http.Serve(ln, srv)
	backend, _ := url.Parse("http://" + ln.Addr().String())

	// A proxy per person adds their session, and opens the menu when asked.
	proxies := map[string]string{}
	for _, who := range []string{"", "ellis", "brooks", "ava", "rachel"} {
		cookie := ""
		if who != "" {
			u, err := st.GetUserByUsername(who)
			if err != nil {
				log.Fatal(err)
			}
			token, _, err := st.CreateSession(u.ID, true, "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_0) Safari/605.1.15", "127.0.0.1")
			if err != nil {
				log.Fatal(err)
			}
			cookie = "taper_session=" + token
		}
		p := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(backend)
			r.Out.Host = r.In.Host
			r.Out.Header.Del("Accept-Encoding")
			if cookie != "" {
				r.Out.Header.Set("Cookie", cookie)
			}
		}}
		p.ModifyResponse = func(res *http.Response) error {
			if res.Request.URL.Query().Get("menu") != "open" || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
				return nil
			}
			b, err := io.ReadAll(res.Body)
			if err != nil {
				return err
			}
			b = bytes.Replace(b, []byte("data-menu>"), []byte("data-menu open>"), 1)
			res.Body = io.NopCloser(bytes.NewReader(b))
			res.ContentLength = int64(len(b))
			res.Header.Del("Content-Length")
			return nil
		}
		pl, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
		go http.Serve(pl, p)
		proxies[who] = "http://" + pl.Addr().String()
	}

	shots := []shot{
		{"home-scholar.png", "ava", "/", desktopW, 860, false},
		{"home-parent.png", "rachel", "/", desktopW, 980, false},
		{"phone-menu.png", "ava", "/?menu=open", phoneW, 720, true},
		{"class-mentor.png", "brooks", fmt.Sprintf("/classes/%d", ids["history"]), desktopW, 900, false},
		{"assignment-scholar.png", "ava", fmt.Sprintf("/assignments/%d", ids["essay"]), desktopW, 900, false},
		{"assignment-roster.png", "brooks", fmt.Sprintf("/assignments/%d#roster", ids["essay"]), desktopW, 900, false},
		{"review.png", "brooks", fmt.Sprintf("/assignments/%d/work/%d", ids["essay"], ids["eli"]), desktopW, 900, false},
		{"chat.png", "ava", fmt.Sprintf("/chat/%d", ids["historyChat"]), desktopW, 760, false},
		{"chat-phone.png", "ava", fmt.Sprintf("/chat/%d", ids["historyChat"]), phoneW, 760, false},
		{"calendar.png", "ava", "/calendar", desktopW, 900, false},
		{"notifications.png", "rachel", "/notifications", desktopW, 560, false},
		{"passkeys.png", "ava", "/account/passkeys", desktopW, 640, false},
		{"sign-in-phone.png", "", "/login", phoneW, 760, false},
		{"person-parent.png", "ellis", fmt.Sprintf("/admin/people/%d", ids["rachel"]), desktopW, 1000, false},
		{"people.png", "ellis", "/admin/people", desktopW, 760, false},
	}
	work, err := os.MkdirTemp("", "taper-shots-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(work)
	for _, s := range shots {
		raw := filepath.Join(work, s.File)
		profile, _ := os.MkdirTemp(work, "profile-")
		// Light theme, no first-run pages.
		os.WriteFile(filepath.Join(profile, "user.js"), []byte(`user_pref("ui.systemUsesDarkTheme", 0);
user_pref("layout.css.prefers-color-scheme.content-override", 1);
user_pref("browser.shell.checkDefaultBrowser", false);
`), 0o644)
		cmd := exec.Command("firefox", "--headless", "--no-remote", "--profile", profile,
			fmt.Sprintf("--window-size=%d,%d", s.Width, s.Height), "--screenshot", raw, proxies[s.As]+s.Path)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		if err := runWithTimeout(cmd, 90*time.Second); err != nil {
			log.Fatalf("%s: %v", s.File, err)
		}
		dst := filepath.Join(out, s.File)
		// 256 colours is plenty for interface pictures, and a quarter of the size.
		if b, err := exec.Command("convert", raw, "-strip", "-dither", "None", "-colors", "256", "PNG8:"+dst).CombinedOutput(); err != nil {
			log.Fatalf("%s: %v\n%s", s.File, err, b)
		}
		fi, _ := os.Stat(dst)
		log.Printf("%-26s %4d KB", s.File, fi.Size()/1024)
	}
}

func runWithTimeout(cmd *exec.Cmd, d time.Duration) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		cmd.Process.Kill()
		return fmt.Errorf("firefox took longer than %s", d)
	}
}

// ---------------------------------------------------------------- the school

func seed(st *store.Store) map[string]int64 {
	ids := map[string]int64{}
	must := func(err error) {
		if err != nil {
			log.Fatal(err)
		}
	}
	hash, err := auth.HashPassword("demo-password-2026")
	must(err)
	must(st.SetSetting("school_name", "Liberty Commonwealth School"))
	must(st.SetSetting("time_zone", "America/Denver"))
	user := func(username, name, role string) *store.User {
		u := &store.User{Username: username, DisplayName: name, Role: role, PasswordHash: hash, Active: true,
			Email: username + "@example.org"}
		must(st.CreateUser(u))
		ids[username] = u.ID
		return u
	}
	ellis := user("ellis", "Margaret Ellis", store.RoleAdmin)
	user("whitfield", "James Whitfield", store.RoleBoard)
	brooks := user("brooks", "Hannah Brooks", store.RoleMentor)
	reyes := user("reyes", "Samuel Reyes", store.RoleMentor)
	ava := user("ava", "Ava Turner", store.RoleScholar)
	eli := user("eli", "Eli Turner", store.RoleScholar)
	var others []*store.User
	for _, n := range [][2]string{{"maya", "Maya Patel"}, {"noah", "Noah Kim"}, {"lily", "Lily Chen"}, {"owen", "Owen Price"},
		{"grace", "Grace Lee"}, {"jonah", "Jonah Ward"}, {"clara", "Clara Hughes"}, {"isaac", "Isaac Moreno"}} {
		others = append(others, user(n[0], n[1], store.RoleScholar))
	}
	rachel := user("rachel", "Rachel Turner", store.RoleParent)
	user("david", "David Patel", store.RoleParent)
	_, _ = st.LinkParent(rachel.ID, ava.ID)
	_, _ = st.LinkParent(rachel.ID, eli.ID)
	_, _ = st.LinkParent(ids["david"], ids["maya"])

	idsOf := func(us ...*store.User) []int64 {
		var out []int64
		for _, u := range us {
			out = append(out, u.ID)
		}
		return out
	}
	class := func(key, name, color, meets, desc string, mentor *store.User, scholars []*store.User) int64 {
		c := &store.Class{Name: name, Color: color, Term: "Fall 2026", Meets: meets, Description: desc}
		must(st.CreateClass(c))
		_, _ = st.AddMembers(c.ID, store.ClassMentor, []int64{mentor.ID})
		_, _ = st.AddMembers(c.ID, store.ClassScholar, idsOf(scholars...))
		ids[key] = c.ID
		return c.ID
	}
	all := append([]*store.User{ava, eli}, others...)
	history := class("history", "History of Liberty", "teal", "Tue & Thu 10:00",
		"We read the founders' own words and ask what liberty asks of us. Bring your commonplace book.", brooks, all)
	latin := class("latin", "Latin I", "blue", "Mon & Wed 9:00", "Grammar, vocabulary and our first stories in Latin.", reyes,
		[]*store.User{ava, others[0], others[1], others[2]})
	class("shakespeare", "Shakespeare Circle", "purple", "Fri 13:00", "This term: *Julius Caesar*. Read aloud, then discuss.", brooks,
		[]*store.User{eli, others[3], others[4], others[5]})
	class("euclid", "Mathematics: Euclid", "gold", "Mon & Wed 11:00", "Book I of the *Elements*, one proposition at a time.", reyes,
		[]*store.User{eli, others[6], others[7]})

	loc, _ := time.LoadLocation("America/Denver")
	today := cal.Today(loc, time.Now())
	// Things happened this morning, at sensible times.
	at := func(h, m int) {
		st.Now = func() time.Time { return time.Date(today.Year(), today.Month(), today.Day(), h, m, 0, 0, loc) }
	}
	defer func() { st.Now = time.Now }()
	at(8, 0)
	day := func(n int) string { return cal.FormatDate(today.AddDate(0, 0, n)) }
	assign := func(key string, classID int64, title, instr string, due int, by *store.User) *store.Assignment {
		a := &store.Assignment{ClassID: classID, Title: title, Instructions: instr, DueDate: day(due), PublishAt: time.Now().Add(-48 * time.Hour).Unix(),
			WorkOnline: true, AllowFiles: true, CreatedBy: by.ID}
		must(st.CreateAssignment(a))
		_, _ = st.MarkAnnounced(a.ID)
		ids[key] = a.ID
		return a
	}
	essay := assign("essay", history, "What did Jefferson mean by \"the pursuit of happiness\"?",
		"Read the Declaration of Independence again, slowly.\n\nIn **one page**, explain what you think Jefferson meant by *the pursuit of happiness*, and whether you agree. Quote the text at least once.", 2, brooks)
	assign("letters", history, "Letter to a founder", "Write a letter to Abigail or John Adams about something in your own life.", 9, brooks)
	assign("timeline", history, "Timeline of 1776", "Make a timeline of ten events from 1776, on paper or here.", -3, brooks)
	decl := assign("declensions", latin, "First declension practice", "Decline *puella*, *aqua* and *via*. Write them out here.", 1, reyes)
	assign("caesar", ids["shakespeare"], "Brutus or Cassius?", "Who is the more honest man in Act I? Give two reasons.", 4, brooks)

	work := func(a *store.Assignment, u *store.User, body, status, feedback string) {
		wk, err := st.StartSubmission(a.ID, u.ID)
		must(err)
		must(st.SaveWork(wk.ID, body))
		if status == store.WorkDraft {
			return
		}
		must(st.SetWorkStatus(wk.ID, []string{store.WorkDraft}, store.WorkTurnedIn, u.ID, nil))
		if status != store.WorkTurnedIn {
			must(st.SetWorkStatus(wk.ID, []string{store.WorkTurnedIn}, status, brooks.ID, &feedback))
		}
	}
	at(9, 40)
	work(essay, ava, "I think Jefferson meant more than being cheerful. He meant", store.WorkDraft, "")
	work(essay, eli, "Jefferson wrote that we have a right to \"the pursuit of happiness\". I think he meant a good life: one where you learn, work hard and help your neighbours, not just one where you have things.\n\nI agree, because a life like that is happy even when it is hard.", store.WorkTurnedIn, "")
	work(essay, others[0], "Happiness for Jefferson was about virtue.", store.WorkComplete, "A thoughtful start. Well argued.")
	work(essay, others[1], "He meant owning land.", store.WorkNeedsWork, "Show me where the text says that. Quote it, then explain.")
	work(essay, others[2], "Freedom to choose your own path.", store.WorkTurnedIn, "")
	work(decl, eli, "puella, puellae, puellae, puellam, puellā", store.WorkComplete, "Optime!")

	com, err := st.CommunityChannel()
	must(err)
	post := func(ch int64, by *store.User, body string) { _, err := st.PostMessage(ch, by.ID, body); must(err) }
	at(7, 30)
	post(com.ID, ellis, "Welcome back, everyone! The fall term starts Monday. Light your taper at mine.")
	post(com.ID, ellis, "The Commonwealth Day picnic is on the calendar. Families welcome.")
	hc, err := st.ClassChannel(history)
	must(err)
	ids["historyChat"] = hc.ID
	for i, m := range []struct {
		by   *store.User
		body string
	}{
		{brooks, "Remember to bring your commonplace books on Thursday."},
		{ava, "Should we quote the Declaration directly in the essay?"},
		{brooks, "Yes, at least once. Short quotations are best."},
		{eli, "Mine's turned in!"},
		{others[0], "Can we read our essays aloud on Thursday?"},
		{brooks, "Good idea, Maya. Volunteers first."},
	} {
		at(10, 2+i*4)
		post(hc.ID, m.by, m.body)
	}
	at(11, 0)
	_ = st.MarkRead(ava.ID, hc.ID, 1<<40)

	event := func(classID int64, title string, d int, start, end, repeat string) {
		e := &store.CalEvent{ClassID: classID, Title: title, StartDate: day(d), EndDate: day(d), StartTime: start, EndTime: end,
			Repeat: repeat, CreatedBy: ellis.ID}
		if repeat == "weekly" {
			e.RepeatDays = fmt.Sprint(int(today.AddDate(0, 0, d).Weekday()))
		}
		must(st.CreateEvent(e))
	}
	event(0, "Commonwealth Day picnic", 6, "", "", "")
	event(0, "Families' evening", 13, "18:30", "20:00", "")
	event(history, "Field trip: the state archives", 8, "09:00", "14:00", "")
	event(latin, "Recitation", 2, "09:00", "09:30", "weekly")

	notice := func(to *store.User, kind, title, link string) {
		_, _, err := st.Notify(store.Notice{Kind: kind, Title: title, Link: link}, []int64{to.ID})
		must(err)
	}
	notice(rachel, store.NotifyCalendar, "New event in History of Liberty: Field trip: the state archives on "+today.AddDate(0, 0, 8).Format("Mon, Jan 2"), "/calendar")
	notice(rachel, store.NotifyWork, "Eli Turner's First declension practice is marked complete", fmt.Sprintf("/assignments/%d?scholar=%d", decl.ID, eli.ID))
	notice(rachel, store.NotifyAssignment, "New assignment in History of Liberty: Letter to a founder", "/classes")
	notice(rachel, store.NotifyDue, "Ava Turner's First declension practice is due tomorrow and isn't turned in yet", fmt.Sprintf("/assignments/%d?scholar=%d", decl.ID, ava.ID))
	notice(ava, store.NotifyDue, "First declension practice is due tomorrow and isn't turned in yet", fmt.Sprintf("/assignments/%d", decl.ID))
	notice(brooks, store.NotifyWork, "Eli Turner turned in the essay", fmt.Sprintf("/assignments/%d#roster", essay.ID))
	return ids
}
