package server

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/taper/internal/cal"
	"github.com/bradyloveland/taper/internal/store"
)

// calEnv is a school with a class, its mentor, a scholar in it, and another scholar.
type calEnv struct {
	*env
	class           *store.Class
	mia, sam, zoe   *store.User
	today, tomorrow string
}

func newCalEnv(t *testing.T) *calEnv {
	e := newEnvReady(t)
	c := &calEnv{env: e}
	c.mia = e.addUser("mia", "Mia Mentor", store.RoleMentor, "mentor-password", false)
	c.sam = e.addUser("sam", "Sam Scholar", store.RoleScholar, "scholar-password", false)
	c.zoe = e.addUser("zoe", "Zoe Zed", store.RoleScholar, "scholar-password", false)
	c.class = &store.Class{Name: "History of Liberty", Color: "teal"}
	e.store.CreateClass(c.class)
	e.store.AddMembers(c.class.ID, store.ClassMentor, []int64{c.mia.ID})
	e.store.AddMembers(c.class.ID, store.ClassScholar, []int64{c.sam.ID})
	today := e.srv.today()
	c.today, c.tomorrow = cal.FormatDate(today), cal.FormatDate(today.AddDate(0, 0, 1))
	return c
}

var eventIDRE = regexp.MustCompile(`^/events/(\d+)\?date=`)

func eventID(t *testing.T, r *resp) string {
	t.Helper()
	m := eventIDRE.FindStringSubmatch(r.Location)
	if r.Status != http.StatusSeeOther || m == nil {
		t.Fatalf("expected a redirect to the event, got %d %q\n%s", r.Status, r.Location, r.Body)
	}
	return m[1]
}

func TestCalendarEvents(t *testing.T) {
	c := newCalEnv(t)
	classKey := fmt.Sprint(c.class.ID)
	a := c.signedIn("admin", "admin-password")
	m := c.signedIn("mia", "mentor-password")
	s := c.signedIn("sam", "scholar-password")
	z := c.signedIn("zoe", "scholar-password")

	expect(t, a.get("/calendar"), http.StatusOK, "My calendar", "Add event", "Subscribe")
	expect(t, a.get("/events/new"), http.StatusOK, "School calendar", "History of Liberty")
	// Validation.
	expect(t, a.postForm("/events/new", url.Values{"calendar": {"school"}, "title": {""}, "start_date": {c.today}}), http.StatusUnprocessableEntity, "Enter a title")
	expect(t, a.postForm("/events/new", url.Values{"calendar": {"school"}, "title": {"X"}, "start_date": {c.tomorrow}, "end_date": {c.today}, "all_day": {"1"}}),
		http.StatusUnprocessableEntity, "end date is before the start date", `id="end_date" name="end_date" class="invalid"`)
	expect(t, a.postForm("/events/new", url.Values{"calendar": {"school"}, "title": {"X"}, "start_date": {c.today}, "start_time": {"18:00"}, "end_time": {"10:00"}}),
		http.StatusUnprocessableEntity, "It ends (10:00 AM) before it starts (6:00 PM)", `id="end_time" name="end_time" class="invalid"`)
	expect(t, a.postForm("/events/new", url.Values{"calendar": {"school"}, "title": {"X"}, "start_date": {c.today}, "end_date": {c.tomorrow}, "all_day": {"1"}, "repeat": {"weekly"}}),
		http.StatusUnprocessableEntity, "same day")

	// A school holiday and a weekly class.
	holiday := eventID(t, a.postForm("/events/new", url.Values{"calendar": {"school"}, "title": {"Founders Day"}, "start_date": {c.tomorrow},
		"end_date": {c.tomorrow}, "all_day": {"1"}, "closed": {"1"}, "description": {"No classes **today**."}}))
	wd := fmt.Sprint(int(c.srv.today().Weekday()))
	weekly := eventID(t, m.postForm("/events/new", url.Values{"calendar": {classKey}, "title": {"Socratic seminar"}, "start_date": {c.today},
		"start_time": {"23:00"}, "end_time": {"23:30"}, "repeat": {"weekly"}, "day": {wd}, "location": {"Library"}}))

	// Who can add where.
	expect(t, m.postForm("/events/new", url.Values{"calendar": {"school"}, "title": {"Nope"}, "start_date": {c.today}, "all_day": {"1"}}), http.StatusForbidden)
	expect(t, s.get("/events/new"), http.StatusForbidden, "can't add events")
	expect(t, s.postForm("/events/new", url.Values{"calendar": {classKey}, "title": {"Nope"}, "start_date": {c.today}, "all_day": {"1"}}), http.StatusForbidden)
	if strings.Contains(s.get("/calendar").Body, "Add event") {
		t.Fatal("scholars can't add events")
	}

	// Views.
	expect(t, s.get("/calendar"), http.StatusOK, "Founders Day", "Socratic seminar", "No school")
	expect(t, s.get("/calendar?view=week"), http.StatusOK, "Socratic seminar", "11:00 PM – 11:30 PM")
	expect(t, s.get("/calendar?view=list"), http.StatusOK, "Founders Day", "Library")
	expect(t, s.get("/calendar?cal=school"), http.StatusOK, "Founders Day")
	if strings.Contains(s.last, "Socratic seminar") {
		t.Fatal("the school calendar shouldn't show class events")
	}
	expect(t, s.get("/calendar?cal=class:"+classKey), http.StatusOK, "Socratic seminar")
	expect(t, z.get("/calendar?cal=class:"+classKey), http.StatusNotFound)
	if strings.Contains(z.get("/calendar").Body, "Socratic seminar") {
		t.Fatal("other scholars don't see the class's events")
	}
	expect(t, z.get("/events/"+weekly), http.StatusNotFound)
	expect(t, s.get("/calendar?date=not-a-date&view=nonsense"), http.StatusOK)

	// Home and class pages show what's coming up.
	expect(t, s.get("/"), http.StatusOK, "Coming up", "Socratic seminar", "Founders Day")
	expect(t, s.get("/classes/"+classKey), http.StatusOK, "Coming up", "Socratic seminar")
	expect(t, m.get("/classes/"+classKey), http.StatusOK, `href="/events/new?cal=`+classKey+`"`)

	// The event page.
	page := s.get("/events/" + holiday)
	expect(t, page, http.StatusOK, "Founders Day", "School calendar", "No school", "<strong>today</strong>")
	if strings.Contains(page.Body, "Delete") {
		t.Fatal("scholars can't delete")
	}
	expect(t, m.get("/events/"+weekly+"?date="+c.today), http.StatusOK, "Every week on", "Remove just this date", "Edit every date")
	expect(t, m.get("/events/"+holiday+"/edit"), http.StatusForbidden)

	// Edit, skip a date, delete.
	expectRedirect(t, m.postForm("/events/"+weekly+"/edit", url.Values{"calendar": {classKey}, "title": {"Seminar"}, "start_date": {c.today},
		"start_time": {"23:00"}, "end_time": {"23:45"}, "repeat": {"weekly"}, "day": {wd}}), "/events/"+weekly+"?date="+c.today)
	expect(t, s.get("/calendar?view=week"), http.StatusOK, "11:00 PM – 11:45 PM")
	expectRedirect(t, m.postForm("/events/"+weekly+"/delete", url.Values{"date": {c.today}}), "/calendar?cal=class:"+classKey)
	expect(t, m.get("/calendar?cal=class:"+classKey), http.StatusOK, "is removed. The other dates stay.")
	nextWeek := cal.FormatDate(c.srv.today().AddDate(0, 0, 7))
	if !strings.Contains(s.get("/calendar?view=list").Body, "Seminar") {
		t.Fatal("later dates should stay")
	}
	if strings.Contains(s.get("/calendar?view=list&date="+c.today).Body, c.srv.today().Format("Monday, January 2")+"</h2>") &&
		strings.Contains(s.last, "Seminar") && !strings.Contains(s.last, nextWeek) {
		t.Fatal("the skipped date should be gone")
	}
	expect(t, s.postForm("/events/"+weekly+"/delete", nil), http.StatusForbidden)
	expectRedirect(t, a.postForm("/events/"+holiday+"/delete", nil), "/calendar?cal=school")
	expect(t, a.get("/events/"+holiday), http.StatusNotFound)
}

func TestCalendarFeeds(t *testing.T) {
	c := newCalEnv(t)
	a := c.signedIn("admin", "admin-password")
	m := c.signedIn("mia", "mentor-password")
	classKey := fmt.Sprint(c.class.ID)
	eventID(t, a.postForm("/events/new", url.Values{"calendar": {"school"}, "title": {"Winter break"}, "start_date": {c.tomorrow},
		"end_date": {cal.FormatDate(c.srv.today().AddDate(0, 0, 3))}, "all_day": {"1"}, "closed": {"1"}}))
	eventID(t, m.postForm("/events/new", url.Values{"calendar": {classKey}, "title": {"Seminar, part 1"}, "start_date": {c.today},
		"start_time": {"10:00"}, "end_time": {"11:00"}, "repeat": {"daily"}, "until": {cal.FormatDate(c.srv.today().AddDate(0, 0, 2))}}))

	s := c.signedIn("sam", "scholar-password")
	linkRE := regexp.MustCompile(`<code id="feed-\d+">(http://[^<]+)</code>`)
	links := func() []string {
		var out []string
		for _, m := range linkRE.FindAllStringSubmatch(s.get("/calendar/subscribe").Body, -1) {
			out = append(out, m[1])
		}
		return out
	}
	page := s.get("/calendar/subscribe")
	expect(t, page, http.StatusOK, "These links are private", "School calendar only", "History of Liberty", "webcal://",
		`id="class-`+classKey+`"`, "Reset this link", "Reset all my links")
	first := links()
	if len(first) != 3 || first[0] == first[1] || !strings.HasSuffix(first[0], ".ics") {
		t.Fatalf("each calendar should have its own link: %v", first)
	}
	if again := links(); strings.Join(again, " ") != strings.Join(first, " ") {
		t.Fatal("the links should stay the same until reset")
	}
	fetch := func(u string) (int, string) {
		res, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, mine := fetch(first[0])
	if code != 200 || !strings.Contains(mine, "BEGIN:VCALENDAR") || !strings.Contains(mine, "SUMMARY:Winter break") ||
		!strings.Contains(mine, `SUMMARY:Seminar\, part 1 (History of Liberty)`) || strings.Count(mine, "BEGIN:VEVENT") != 4 ||
		!strings.Contains(mine, "X-WR-CALNAME:Liberty Commonwealth: My calendar") || !strings.Contains(mine, "CATEGORIES:No school") {
		t.Fatalf("my feed (%d):\n%s", code, mine)
	}
	if !regexp.MustCompile(`UID:\d{4}-\d\d-\d\d-[a-z0-9_-]+@taper`).MatchString(mine) {
		t.Fatal("repeating occurrences need their own UIDs")
	}
	_, school := fetch(first[1])
	if strings.Contains(school, "Seminar") || !strings.Contains(school, "Winter break") {
		t.Fatalf("school feed:\n%s", school)
	}
	_, class := fetch(first[2])
	if strings.Contains(class, "Winter break") || !strings.Contains(class, "SUMMARY:Seminar\\, part 1\r\n") {
		t.Fatalf("class feed:\n%s", class)
	}

	// Wrong tokens are not found.
	for _, u := range []string{c.ts.URL + "/ical/nope.ics", strings.TrimSuffix(first[0], ".ics"), c.ts.URL + "/ical/.ics"} {
		if code, _ := fetch(u); code != 404 {
			t.Errorf("%s: %d", u, code)
		}
	}
	// Leaving the class stops its link.
	c.store.RemoveMember(c.class.ID, c.sam.ID)
	if code, _ := fetch(first[2]); code != 404 {
		t.Fatal("a class link stops when its owner leaves the class")
	}
	c.store.AddMembers(c.class.ID, store.ClassScholar, []int64{c.sam.ID})

	// Resetting one link stops only that one.
	expectRedirect(t, s.postForm("/calendar/subscribe/reset", url.Values{"scope": {"school"}}), "/calendar/subscribe#school")
	expect(t, s.get("/calendar/subscribe"), http.StatusOK, "The link for School calendar only is new", "other links are unchanged")
	second := links()
	if second[1] == first[1] || second[0] != first[0] || second[2] != first[2] {
		t.Fatalf("only the school link should change:\n%v\n%v", first, second)
	}
	if code, _ := fetch(first[1]); code != 404 {
		t.Fatal("the old school link should stop working")
	}
	if code, _ := fetch(first[0]); code != 200 {
		t.Fatal("the other links should keep working")
	}
	expect(t, s.postForm("/calendar/subscribe/reset", url.Values{"scope": {"class:9999"}}), http.StatusNotFound)
	// Resetting all.
	expectRedirect(t, s.postForm("/calendar/subscribe/reset", url.Values{"scope": {"all"}}), "/calendar/subscribe")
	third := links()
	for i := range third {
		if third[i] == second[i] {
			t.Fatal("every link should be new")
		}
		if code, _ := fetch(second[i]); code != 404 {
			t.Fatal("every old link should stop")
		}
	}
	// Deactivating stops them too.
	c.sam.Active = false
	c.store.UpdateUser(c.sam)
	if code, _ := fetch(third[0]); code != 404 {
		t.Fatal("a deactivated person's links should stop")
	}
}

func TestTimeZoneSetting(t *testing.T) {
	c := newCalEnv(t)
	a := c.signedIn("admin", "admin-password")
	expect(t, a.get("/admin/settings"), http.StatusOK, "Time zone", "America/Denver")
	expect(t, a.postForm("/admin/settings", url.Values{"school": {"Liberty"}, "time_zone": {"Mars/Olympus"}}), http.StatusUnprocessableEntity, "time zone isn't one")
	expectRedirect(t, a.postForm("/admin/settings", url.Values{"school": {"Liberty"}, "time_zone": {"Pacific/Auckland"}}), "/admin/settings")
	if c.srv.loc().String() != "Pacific/Auckland" {
		t.Fatalf("zone %s", c.srv.loc())
	}
	want := cal.FormatDate(cal.Today(c.srv.loc(), time.Now()))
	if cal.FormatDate(c.srv.today()) != want {
		t.Fatal("today should follow the school's time zone")
	}
	expectRedirect(t, a.postForm("/admin/settings", url.Values{"school": {"Liberty"}, "time_zone": {""}}), "/admin/settings")
	if c.srv.loc() != time.Local {
		t.Fatal("empty means the server's zone")
	}
}
