package server

import (
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bradyloveland/taper/internal/cal"
	"github.com/bradyloveland/taper/internal/store"
)

// TestPerformance fills a school bigger than most will be, and times the
// main pages for each kind of person. It's slow to set up, so it runs only
// with TAPER_PERF=1:
//
//	TAPER_PERF=1 go test ./internal/server -run TestPerformance -v
//
// Each page must answer within perfBudget (the median of several loads).
const perfBudget = 150 * time.Millisecond

func TestPerformance(t *testing.T) {
	if os.Getenv("TAPER_PERF") == "" {
		t.Skip("set TAPER_PERF=1 to run")
	}
	e := newEnvReady(t)
	st := e.store
	rng := rand.New(rand.NewSource(1))
	start := time.Now()
	add := func(prefix string, n int, role string) []*store.User {
		var out []*store.User
		for i := 0; i < n; i++ {
			out = append(out, e.addUser(fmt.Sprintf("%s%d", prefix, i), fmt.Sprintf("%s Person %d", strings.ToUpper(prefix[:1])+prefix[1:], i),
				role, "perf-password", false))
		}
		return out
	}
	mentors := add("mentor", 40, store.RoleMentor)
	scholars := add("scholar", 400, store.RoleScholar)
	parents := add("parent", 200, store.RoleParent)
	for i, p := range parents {
		st.LinkParent(p.ID, scholars[(2*i)%len(scholars)].ID)
		st.LinkParent(p.ID, scholars[(2*i+1)%len(scholars)].ID)
	}
	today := e.srv.today()
	var classes []*store.Class
	var assignments []*store.Assignment
	members := map[int64][]int64{}
	for i := 0; i < 60; i++ {
		c := &store.Class{Name: fmt.Sprintf("Class %02d", i), Color: "teal", Term: "Fall"}
		st.CreateClass(c)
		classes = append(classes, c)
		st.AddMembers(c.ID, store.ClassMentor, []int64{mentors[i%40].ID, mentors[(i+7)%40].ID})
		var ids []int64
		for _, k := range rng.Perm(len(scholars))[:25] {
			ids = append(ids, scholars[k].ID)
		}
		st.AddMembers(c.ID, store.ClassScholar, ids)
		members[c.ID] = ids
		for j := 0; j < 30; j++ {
			a := &store.Assignment{ClassID: c.ID, Title: fmt.Sprintf("Assignment %d", j), Instructions: "Read and respond.",
				DueDate: cal.FormatDate(today.AddDate(0, 0, rng.Intn(120)-60)), PublishAt: 1, WorkOnline: true, CreatedBy: mentors[i%40].ID}
			st.CreateAssignment(a)
			st.MarkAnnounced(a.ID) // as the upgrade does for assignments already out
			assignments = append(assignments, a)
		}
	}
	work := 0
	for _, a := range assignments {
		for _, sid := range members[a.ClassID] {
			if rng.Intn(10) < 7 {
				wk, _ := st.StartSubmission(a.ID, sid)
				st.SaveWork(wk.ID, "My answer, with a few sentences of writing in it.")
				st.SetWorkStatus(wk.ID, []string{store.WorkDraft}, store.WorkTurnedIn, sid, nil)
				if rng.Intn(2) == 0 {
					fb := "Good work."
					st.SetWorkStatus(wk.ID, []string{store.WorkTurnedIn}, store.WorkComplete, mentors[0].ID, &fb)
				}
				work++
			}
		}
	}
	messages := 0
	com, _ := st.CommunityChannel()
	for i := 0; i < 2000; i++ {
		st.PostMessage(com.ID, 1, fmt.Sprintf("News item %d", i))
		messages++
	}
	for _, c := range classes {
		ch, _ := st.ClassChannel(c.ID)
		for i := 0; i < 300; i++ {
			st.PostMessage(ch.ID, members[c.ID][i%25], fmt.Sprintf("Message %d in %s", i, c.Name))
			messages++
		}
	}
	for i := 0; i < 500; i++ {
		d := cal.FormatDate(today.AddDate(0, 0, rng.Intn(180)-90))
		ev := &store.CalEvent{Title: fmt.Sprintf("Event %d", i), StartDate: d, EndDate: d, CreatedBy: 1}
		if i%3 != 0 {
			ev.ClassID = classes[i%60].ID
		}
		if i%10 == 0 {
			ev.Repeat = "weekly"
		}
		st.CreateEvent(ev)
	}
	everyone := make([]int64, 0, 640)
	for _, l := range [][]*store.User{mentors, scholars, parents} {
		for _, u := range l {
			everyone = append(everyone, u.ID)
		}
	}
	for i := 0; i < 60; i++ {
		st.Notify(store.Notice{Kind: store.NotifyChat, Ref: fmt.Sprintf("chat:%d", i%10), Title: "A message", Many: "{n} new messages"}, everyone)
		st.Notify(store.Notice{Kind: store.NotifyCalendar, Title: fmt.Sprintf("Event %d changed", i)}, everyone)
	}
	t.Logf("seeded %d people, %d classes, %d assignments, %d pieces of work, %d messages in %s",
		len(everyone)+1, len(classes), len(assignments), work, messages, time.Since(start).Round(time.Second))

	// The busiest people: a mentor of two classes, a scholar in several, a parent of two.
	c0 := classes[0]
	a0 := assignments[0]
	ch0, _ := st.ClassChannel(c0.ID)
	scholar := mustUser(t, e, "scholar0")
	sc, _ := st.ListClassesFor(scholar.ID)
	if len(sc) > 0 {
		c0 = sc[0]
		ch0, _ = st.ClassChannel(c0.ID)
		list, _ := st.ListAssignments(store.AssignmentQuery{ClassIDs: []int64{c0.ID}})
		a0 = list[0]
	}
	pages := map[string][]string{
		"admin": {"/", "/classes", "/admin/people", "/admin/people?role=scholar", "/chat", fmt.Sprintf("/chat/%d", com.ID),
			"/calendar", "/calendar?view=list", "/notifications"},
		"mentor0": {"/", "/classes", fmt.Sprintf("/classes/%d", classes[0].ID), fmt.Sprintf("/classes/%d/assignments", classes[0].ID),
			fmt.Sprintf("/assignments/%d", assignments[0].ID), fmt.Sprintf("/assignments/%d/work/%d", assignments[0].ID, members[classes[0].ID][0]),
			"/calendar", "/chat", "/notifications"},
		"scholar0": {"/", "/classes", fmt.Sprintf("/classes/%d", c0.ID), fmt.Sprintf("/classes/%d/assignments", c0.ID),
			fmt.Sprintf("/assignments/%d", a0.ID), "/calendar", "/calendar?view=week", "/chat", fmt.Sprintf("/chat/%d", ch0.ID),
			"/notifications", "/account"},
		"parent0": {"/", "/classes", fmt.Sprintf("/classes/%d", c0.ID), "/calendar", "/chat", "/notifications"},
	}
	var report []string
	var slow []string
	for who, list := range pages {
		pw := "perf-password"
		if who == "admin" {
			pw = "admin-password"
		}
		b := e.signedIn(who, pw)
		for _, p := range list {
			var times []time.Duration
			for i := 0; i < 5; i++ {
				t0 := time.Now()
				r := b.get(p)
				times = append(times, time.Since(t0))
				if r.Status != http.StatusOK {
					t.Fatalf("%s %s: %d", who, p, r.Status)
				}
			}
			sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
			med := times[len(times)/2]
			report = append(report, fmt.Sprintf("%-9s %-40s %6.1f ms %5d KB", who, p, float64(med.Microseconds())/1000, len(b.last)/1024))
			if med > perfBudget {
				slow = append(slow, fmt.Sprintf("%s %s: %s", who, p, med.Round(time.Millisecond)))
			}
		}
	}
	// Writes that tell many people: a Community post (everyone), a class
	// message, publishing an assignment, and feedback.
	adm := e.signedIn("admin", "admin-password")
	men := e.signedIn("mentor0", "perf-password")
	timeIt := func(what string, limit time.Duration, f func() *resp) {
		t.Helper()
		t0 := time.Now()
		r := f()
		d := time.Since(t0)
		e.srv.wg.Wait() // and the email and pushes it started
		if r.Status >= 400 {
			t.Fatalf("%s: %d %s", what, r.Status, r.Body)
		}
		report = append(report, fmt.Sprintf("~write    %-40s %6.1f ms", what, float64(d.Microseconds())/1000))
		if d > limit {
			slow = append(slow, fmt.Sprintf("%s: %s", what, d.Round(time.Millisecond)))
		}
	}
	adm.get(fmt.Sprintf("/chat/%d", com.ID))
	timeIt("Community post (641 people)", 500*time.Millisecond, func() *resp {
		return adm.postJSON(fmt.Sprintf("/chat/%d", com.ID), map[string][]string{"body": {"School's closed Friday."}})
	})
	men.get(fmt.Sprintf("/chat/%d", ch0.ID))
	cch, _ := st.ClassChannel(classes[0].ID)
	men.get(fmt.Sprintf("/chat/%d", cch.ID))
	timeIt("class chat message", perfBudget, func() *resp {
		return men.postJSON(fmt.Sprintf("/chat/%d", cch.ID), map[string][]string{"body": {"See you tomorrow."}})
	})
	timeIt("publish an assignment", perfBudget, func() *resp {
		return men.postFiles(fmt.Sprintf("/classes/%d/assignments/new", classes[0].ID),
			map[string][]string{"title": {"New one"}, "publish": {"now"}, "work_online": {"1"}}, nil)
	})
	timeIt("feedback on work", perfBudget, func() *resp {
		return men.postForm(fmt.Sprintf("/assignments/%d/work/%d", assignments[0].ID, members[classes[0].ID][0]),
			map[string][]string{"decision": {"needs_work"}, "feedback": {"One more example."}})
	})
	t0 := time.Now()
	e.srv.noticeTick(time.Date(today.Year(), today.Month(), today.Day(), 18, 30, 0, 0, e.srv.loc()))
	report = append(report, fmt.Sprintf("~write    %-40s %6.1f ms", "the minute's background work at 6:30 PM", float64(time.Since(t0).Microseconds())/1000))

	sort.Strings(report)
	t.Logf("median of 5 loads:\n%s", strings.Join(report, "\n"))
	if len(slow) > 0 {
		t.Errorf("slower than %s:\n%s", perfBudget, strings.Join(slow, "\n"))
	}
}
