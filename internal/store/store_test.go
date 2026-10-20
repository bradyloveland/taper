package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "taper.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsAndSettings(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "taper.db"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.SchemaVersion()
	if err != nil || v < 1 {
		t.Fatalf("schema version %d, %v", v, err)
	}
	if s.SchoolName() != "" {
		t.Fatal("school name should start empty")
	}
	if err := s.SetSetting("school_name", "Liberty Commonwealth"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Reopening applies nothing new and keeps data.
	s, err = Open(filepath.Join(dir, "taper.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v2, _ := s.SchemaVersion(); v2 != v {
		t.Fatalf("schema version changed on reopen: %d -> %d", v, v2)
	}
	if s.SchoolName() != "Liberty Commonwealth" {
		t.Fatal("setting lost")
	}
	if err := s.Backup(filepath.Join(dir, "copy.db")); err != nil {
		t.Fatal(err)
	}
	c, err := Open(filepath.Join(dir, "copy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.SchoolName() != "Liberty Commonwealth" {
		t.Fatal("backup is missing data")
	}
}

func TestUsers(t *testing.T) {
	s := openTest(t)
	a := &User{Username: "Ann", DisplayName: "Ann Adams", Role: RoleAdmin, PasswordHash: "x", Active: true}
	if err := s.CreateUser(a); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(&User{Username: "ann", DisplayName: "Dup", Role: RoleScholar, PasswordHash: "x", Active: true}); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate username (any case) should fail, got %v", err)
	}
	if err := s.CreateUser(&User{Username: "bo", DisplayName: "Bo", Role: "wizard", PasswordHash: "x"}); err == nil {
		t.Fatal("bad role accepted")
	}
	m := &User{Username: "mia", DisplayName: "Mia Moss", Email: "mia@example.org", Role: RoleMentor, PasswordHash: "x", Active: true}
	sc := &User{Username: "sam", DisplayName: "Sam 100%_Stone", Role: RoleScholar, PasswordHash: "x", Active: true}
	for _, u := range []*User{m, sc} {
		if err := s.CreateUser(u); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetUserByUsername(" ANN ")
	if err != nil || got.ID != a.ID || !got.IsAdmin() || !got.IsMentor() {
		t.Fatalf("lookup by username: %+v %v", got, err)
	}
	if _, err := s.GetUser(999); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing user should be ErrNotFound")
	}
	list, _ := s.ListUsers(UserFilter{})
	if len(list) != 3 || list[0].Username != "Ann" || list[2].Username != "sam" {
		t.Fatalf("list order wrong: %v", list)
	}
	list, _ = s.ListUsers(UserFilter{Role: RoleMentor})
	if len(list) != 1 || list[0].ID != m.ID {
		t.Fatal("role filter wrong")
	}
	list, _ = s.ListUsers(UserFilter{Query: "example.org"})
	if len(list) != 1 || list[0].ID != m.ID {
		t.Fatal("email search wrong")
	}
	list, _ = s.ListUsers(UserFilter{Query: "100%_"})
	if len(list) != 1 || list[0].ID != sc.ID {
		t.Fatal("LIKE wildcards should be literal")
	}
	list, _ = s.ListUsers(UserFilter{Query: "%"})
	if len(list) != 1 {
		t.Fatalf("%% should match only literally, got %d", len(list))
	}

	sc.Active = false
	sc.DisplayName = "Sam Stone"
	if err := s.UpdateUser(sc); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.ListUsers(UserFilter{}); len(list) != 2 {
		t.Fatal("inactive users should be hidden by default")
	}
	if list, _ = s.ListUsers(UserFilter{Status: "inactive"}); len(list) != 1 || list[0].DisplayName != "Sam Stone" {
		t.Fatal("inactive filter wrong")
	}
	m.Username = "ANN"
	if err := s.UpdateUser(m); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("rename to taken username: %v", err)
	}
	counts, _ := s.RoleCounts()
	if counts[RoleAdmin] != 1 || counts[RoleMentor] != 1 || counts[RoleScholar] != 0 {
		t.Fatalf("counts %v", counts)
	}
	if n, _ := s.ActiveAdmins(); n != 1 {
		t.Fatal("active admins")
	}
	if err := s.SetPassword(m.ID, "newhash", true); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUser(m.ID)
	if got.PasswordHash != "newhash" || !got.MustChangePassword {
		t.Fatal("password not saved")
	}
	if (&User{DisplayName: "mary ann smith"}).Initials() != "MA" || (&User{}).Initials() != "?" {
		t.Fatal("initials")
	}
}

func TestSessions(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_700_000_000, 0)
	s.Now = func() time.Time { return now }
	u := &User{Username: "ann", DisplayName: "Ann", Role: RoleScholar, PasswordHash: "x", Active: true}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	short, _, err := s.CreateSession(u.ID, false, "Firefox", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	long, longSess, err := s.CreateSession(u.ID, true, "Safari", "10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if _, got, err := s.LookupSession(short); err != nil || got.ID != u.ID {
		t.Fatalf("lookup: %v", err)
	}
	if _, _, err := s.LookupSession("bogus"); !errors.Is(err, ErrNotFound) {
		t.Fatal("bogus token found")
	}
	// After 13 hours the short session is gone; the remembered one slides.
	now = now.Add(13 * time.Hour)
	if _, _, err := s.LookupSession(short); !errors.Is(err, ErrNotFound) {
		t.Fatal("short session should have expired")
	}
	sess, _, err := s.LookupSession(long)
	if err != nil || sess.ExpiresAt <= longSess.ExpiresAt {
		t.Fatalf("remembered session should slide: %v", err)
	}
	// Deactivated users can't use their sessions.
	u.Active = false
	_ = s.UpdateUser(u)
	if _, _, err := s.LookupSession(long); !errors.Is(err, ErrNotFound) {
		t.Fatal("inactive user's session still works")
	}
	u.Active = true
	_ = s.UpdateUser(u)
	other, otherSess, _ := s.CreateSession(u.ID, true, "Chrome", "")
	list, _ := s.UserSessions(u.ID)
	if len(list) != 2 {
		t.Fatalf("sessions: %d", len(list))
	}
	if err := s.DeleteUserSessions(u.ID, otherSess.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LookupSession(long); err == nil {
		t.Fatal("other sessions should be signed out")
	}
	if _, _, err := s.LookupSession(other); err != nil {
		t.Fatal("kept session was signed out")
	}
	now = now.Add(31 * 24 * time.Hour)
	if err := s.PruneSessions(); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.UserSessions(u.ID); len(list) != 0 {
		t.Fatal("prune left sessions")
	}
}

func TestBugReports(t *testing.T) {
	s := openTest(t)
	u := &User{Username: "sam", DisplayName: "Sam Stone", Role: RoleScholar, PasswordHash: "x", Active: true}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	a := &BugReport{UserID: u.ID, Title: "Button broken", Body: "body", Page: "/account"}
	if err := s.CreateBugReport(a); err != nil || a.ID == 0 {
		t.Fatal(err)
	}
	b := &BugReport{Title: "Anonymous", Body: "x"}
	if err := s.CreateBugReport(b); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UnsentBugReports(); n != 2 {
		t.Fatalf("unsent = %d", n)
	}
	if err := s.SetBugReportIssue(a.ID, "https://github.com/o/r/issues/7", 7, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetBugReport(a.ID)
	if err != nil || got.Reporter != "Sam Stone" || got.IssueNumber != 7 || got.Page != "/account" {
		t.Fatalf("got %+v %v", got, err)
	}
	list, _ := s.ListBugReports(10)
	if len(list) != 2 || list[0].ID != b.ID || list[1].Reporter != "Sam Stone" {
		t.Fatalf("list %+v", list)
	}
	if n, _ := s.UnsentBugReports(); n != 1 {
		t.Fatalf("unsent = %d", n)
	}
	if _, err := s.GetBugReport(999); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing report")
	}
}

func TestTwoStepStore(t *testing.T) {
	s := openTest(t)
	u := &User{Username: "ann", DisplayName: "Ann", Role: RoleMentor, PasswordHash: "x", Active: true}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableTOTP(u.ID, "sealed", 100, []string{"h1", "h2"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetUser(u.ID)
	if !got.TOTPEnabled || got.TOTPSecret != "sealed" || got.TOTPLastStep != 100 {
		t.Fatalf("user %+v", got)
	}
	if ok, _ := s.UseTOTPStep(u.ID, 100); ok {
		t.Fatal("a used step was accepted again")
	}
	if ok, _ := s.UseTOTPStep(u.ID, 101); !ok {
		t.Fatal("a new step was refused")
	}
	if ok, _ := s.UseRecoveryCode(u.ID, "h1"); !ok {
		t.Fatal("recovery code refused")
	}
	if ok, _ := s.UseRecoveryCode(u.ID, "h1"); ok {
		t.Fatal("recovery code used twice")
	}
	if ok, _ := s.UseRecoveryCode(u.ID, "nope"); ok {
		t.Fatal("unknown recovery code accepted")
	}
	if n, _ := s.RecoveryCodesLeft(u.ID); n != 1 {
		t.Fatalf("left %d", n)
	}
	if err := s.ReplaceRecoveryCodes(u.ID, []string{"a", "b", "c"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.RecoveryCodesLeft(u.ID); n != 3 {
		t.Fatalf("left %d", n)
	}
	if err := s.DisableTOTP(u.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUser(u.ID)
	if got.TOTPEnabled || got.TOTPSecret != "" {
		t.Fatal("not disabled")
	}
	if n, _ := s.RecoveryCodesLeft(u.ID); n != 0 {
		t.Fatal("codes left after disabling")
	}
}

func TestPendingSessions(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_700_000_000, 0)
	s.Now = func() time.Time { return now }
	u := &User{Username: "ann", DisplayName: "Ann", Role: RoleMentor, PasswordHash: "x", Active: true}
	s.CreateUser(u)
	tok, sess, err := s.CreatePendingSession(u.ID, true, "ua", "ip")
	if err != nil || !sess.MFAPending {
		t.Fatal(err)
	}
	got, _, err := s.LookupSession(tok)
	if err != nil || !got.MFAPending {
		t.Fatalf("pending lookup: %+v %v", got, err)
	}
	if list, _ := s.UserSessions(u.ID); len(list) != 0 {
		t.Fatal("pending sessions aren't listed as signed in")
	}
	if err := s.CompleteMFA(sess.TokenHash); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.LookupSession(tok)
	if got.MFAPending || got.ExpiresAt-now.Unix() < int64(29*24*time.Hour/time.Second) {
		t.Fatalf("completed session: %+v", got)
	}
	if err := s.CompleteMFA(sess.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatal("completing twice")
	}
	// An unfinished one expires after ten minutes.
	tok2, _, _ := s.CreatePendingSession(u.ID, true, "ua", "ip")
	now = now.Add(11 * time.Minute)
	if _, _, err := s.LookupSession(tok2); !errors.Is(err, ErrNotFound) {
		t.Fatal("pending session should expire")
	}
}

func TestPasswordResets(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_700_000_000, 0)
	s.Now = func() time.Time { return now }
	u := &User{Username: "ann", DisplayName: "Ann", Role: RoleMentor, PasswordHash: "x", Active: true}
	s.CreateUser(u)
	first, _ := s.CreatePasswordReset(u.ID)
	tok, err := s.CreatePasswordReset(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PasswordResetUser(first); err == nil {
		t.Fatal("an older link should stop working")
	}
	if got, err := s.PasswordResetUser(tok); err != nil || got.ID != u.ID {
		t.Fatalf("lookup: %v", err)
	}
	if ok, _ := s.UsePasswordReset(tok); !ok {
		t.Fatal("use refused")
	}
	if ok, _ := s.UsePasswordReset(tok); ok {
		t.Fatal("used twice")
	}
	if _, err := s.PasswordResetUser(tok); err == nil {
		t.Fatal("a used link still works")
	}
	tok, _ = s.CreatePasswordReset(u.ID)
	now = now.Add(61 * time.Minute)
	if _, err := s.PasswordResetUser(tok); err == nil {
		t.Fatal("an expired link still works")
	}
	tok, _ = s.CreatePasswordReset(u.ID)
	u.Active = false
	s.UpdateUser(u)
	if _, err := s.PasswordResetUser(tok); err == nil {
		t.Fatal("a deactivated account's link still works")
	}
}

func TestClasses(t *testing.T) {
	s := openTest(t)
	mk := func(username, name, role string) *User {
		u := &User{Username: username, DisplayName: name, Role: role, PasswordHash: "x", Active: true}
		if err := s.CreateUser(u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	mia := mk("mia", "Mia Moss", RoleMentor)
	ann := mk("ann", "Ann Admin", RoleAdmin)
	sam := mk("sam", "Sam Stone", RoleScholar)
	zoe := mk("zoe", "Zoe Zed", RoleScholar)

	hist := &Class{Name: "History of Liberty", Term: "2026–27", Color: "teal", Description: "**Big** ideas"}
	math := &Class{Name: "Arithmetic", Term: "2026–27", Color: "blue"}
	old := &Class{Name: "Old Latin", Term: "2025–26", Color: "gray", Archived: true}
	for _, c := range []*Class{hist, math, old} {
		if err := s.CreateClass(c); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.AddMembers(hist.ID, ClassMentor, []int64{mia.ID, ann.ID}); err != nil || n != 2 {
		t.Fatalf("add mentors: %d %v", n, err)
	}
	if n, _ := s.AddMembers(hist.ID, ClassScholar, []int64{sam.ID, zoe.ID}); n != 2 {
		t.Fatal("add scholars")
	}
	if n, _ := s.AddMembers(hist.ID, ClassScholar, []int64{sam.ID}); n != 0 {
		t.Fatal("adding again should change nothing")
	}
	s.AddMembers(math.ID, ClassScholar, []int64{sam.ID})
	s.AddMembers(old.ID, ClassScholar, []int64{sam.ID})

	got, err := s.GetClass(hist.ID)
	if err != nil || got.Scholars != 2 || got.MentorNames() != "Ann Admin, Mia Moss" || got.Description != "**Big** ideas" {
		t.Fatalf("class %+v %v", got, err)
	}
	list, _ := s.ListClasses(ClassFilter{})
	if len(list) != 2 || list[0].Name != "Arithmetic" || list[1].Scholars != 2 {
		t.Fatalf("list %+v", list)
	}
	if list, _ = s.ListClasses(ClassFilter{Archived: true}); len(list) != 1 || list[0].ID != old.ID {
		t.Fatal("archived list")
	}
	if list, _ = s.ListClasses(ClassFilter{Query: "lib"}); len(list) != 1 || list[0].ID != hist.ID {
		t.Fatal("search")
	}
	if list, _ = s.ListClasses(ClassFilter{Term: "2025–26"}); len(list) != 0 {
		t.Fatal("term filter should still hide archived")
	}
	mine, _ := s.ListClassesFor(sam.ID)
	if len(mine) != 2 || mine[0].MyRole != ClassScholar {
		t.Fatalf("sam's classes %+v", mine)
	}
	mine, _ = s.ListClassesFor(mia.ID)
	if len(mine) != 1 || mine[0].MyRole != ClassMentor {
		t.Fatal("mia's classes")
	}
	members, _ := s.ClassMembers(hist.ID, false)
	if len(members) != 4 || members[0].ClassRole != ClassMentor || members[2].Username != "sam" {
		t.Fatalf("members %v", members)
	}
	if r, _ := s.ClassRole(hist.ID, zoe.ID); r != ClassScholar {
		t.Fatal("role")
	}
	if r, _ := s.ClassRole(math.ID, zoe.ID); r != "" {
		t.Fatal("not a member")
	}
	// Deactivated people drop out of counts and default lists.
	zoe.Active = false
	s.UpdateUser(zoe)
	if got, _ := s.GetClass(hist.ID); got.Scholars != 1 {
		t.Fatal("inactive scholar counted")
	}
	if m, _ := s.ClassMembers(hist.ID, true); len(m) != 4 {
		t.Fatal("withInactive")
	}
	s.RemoveMember(hist.ID, sam.ID)
	if r, _ := s.ClassRole(hist.ID, sam.ID); r != "" {
		t.Fatal("removed")
	}
	terms, _ := s.Terms()
	if len(terms) != 2 {
		t.Fatalf("terms %v", terms)
	}
	if n, _ := s.CountClasses(); n != 2 {
		t.Fatal("count")
	}
	if err := s.DeleteClass(hist.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetClass(hist.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted")
	}
	if m, _ := s.ClassMembers(hist.ID, true); len(m) != 0 {
		t.Fatal("memberships should go with the class")
	}
	if !ValidColor("teal") || ValidColor("pink") {
		t.Fatal("colors")
	}
}

func TestEvents(t *testing.T) {
	s := openTest(t)
	u := &User{Username: "mia", DisplayName: "Mia", Role: RoleMentor, PasswordHash: "x", Active: true}
	s.CreateUser(u)
	c := &Class{Name: "History", Color: "teal"}
	s.CreateClass(c)
	other := &Class{Name: "Math", Color: "blue"}
	s.CreateClass(other)

	school := &CalEvent{Title: "Winter break", Closed: true, StartDate: "2026-12-21", EndDate: "2027-01-01", CreatedBy: u.ID}
	weekly := &CalEvent{ClassID: c.ID, Title: "History", StartDate: "2026-09-01", StartTime: "10:00", EndDate: "2026-09-01",
		EndTime: "11:30", Repeat: "weekly", RepeatDays: "2,4", RepeatUntil: "2027-05-31"}
	math := &CalEvent{ClassID: other.ID, Title: "Math test", StartDate: "2026-10-15", EndDate: "2026-10-15"}
	for _, e := range []*CalEvent{school, weekly, math} {
		if err := s.CreateEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	if school.UID == weekly.UID || !strings.HasSuffix(school.UID, "@taper") {
		t.Fatal("uids")
	}
	got, err := s.ListEvents(EventQuery{School: true, ClassIDs: []int64{c.ID}, From: "2026-10-01", To: "2026-10-31"})
	if err != nil || len(got) != 1 || got[0].ID != weekly.ID || got[0].ClassName != "History" || got[0].ClassColor != "teal" {
		t.Fatalf("october: %+v %v", got, err)
	}
	got, _ = s.ListEvents(EventQuery{School: true, ClassIDs: []int64{c.ID}, From: "2026-12-01", To: "2026-12-31"})
	if len(got) != 2 {
		t.Fatalf("december: %d", len(got))
	}
	if got, _ := s.ListEvents(EventQuery{}); got != nil {
		t.Fatal("no calendars, no events")
	}
	if got, _ := s.ListEvents(EventQuery{ClassIDs: []int64{c.ID}, From: "2027-09-01", To: "2027-09-30"}); len(got) != 0 {
		t.Fatal("after the repeat ends")
	}

	if err := s.SkipDate(weekly.ID, "2026-10-08"); err != nil {
		t.Fatal(err)
	}
	e, _ := s.GetEvent(weekly.ID)
	if !e.Skips["2026-10-08"] || e.Sequence != 1 {
		t.Fatalf("skip: %+v", e)
	}
	r, err := e.Rule()
	if err != nil {
		t.Fatal(err)
	}
	occ := r.Occurrences(mustDate("2026-10-05"), mustDate("2026-10-11"))
	if len(occ) != 1 || occ[0].Format("2006-01-02") != "2026-10-06" {
		t.Fatalf("occurrences: %v", occ)
	}
	e.Title = "History of Liberty"
	if err := s.UpdateEvent(e); err != nil {
		t.Fatal(err)
	}
	e, _ = s.GetEvent(weekly.ID)
	if e.Title != "History of Liberty" || e.Sequence != 2 {
		t.Fatal("update")
	}
	s.DeleteClass(c.ID)
	if _, err := s.GetEvent(weekly.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("class events go with the class")
	}
	s.DeleteEvent(school.ID)
	if _, err := s.GetEvent(school.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete")
	}

	// Calendar links, one per calendar.
	if sealed, _ := s.CalendarLink(u.ID, "mine"); sealed != "" {
		t.Fatal("no link yet")
	}
	s.SetCalendarLink(u.ID, "mine", "h-mine", "s-mine")
	s.SetCalendarLink(u.ID, "school", "h-school", "s-school")
	s.SetCalendarLink(u.ID, "mine", "h-mine2", "s-mine2")
	if sealed, _ := s.CalendarLink(u.ID, "mine"); sealed != "s-mine2" {
		t.Fatal("link replaced")
	}
	if _, _, err := s.CalendarLinkByToken("h-mine"); err == nil {
		t.Fatal("the old token still works")
	}
	if got, scope, err := s.CalendarLinkByToken("h-school"); err != nil || got.ID != u.ID || scope != "school" {
		t.Fatal("resetting one link shouldn't touch the others")
	}
	s.DeleteCalendarLinks(u.ID)
	if _, _, err := s.CalendarLinkByToken("h-school"); err == nil {
		t.Fatal("reset all")
	}
	s.SetCalendarLink(u.ID, "mine", "h3", "s3")
	u.Active = false
	s.UpdateUser(u)
	if _, _, err := s.CalendarLinkByToken("h3"); err == nil {
		t.Fatal("deactivated people's links stop")
	}
}

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestAssignments(t *testing.T) {
	s := openTest(t)
	mentor := &User{Username: "mia", DisplayName: "Mia", Role: RoleMentor, PasswordHash: "x", Active: true}
	sam := &User{Username: "sam", DisplayName: "Sam", Role: RoleScholar, PasswordHash: "x", Active: true}
	for _, u := range []*User{mentor, sam} {
		if err := s.CreateUser(u); err != nil {
			t.Fatal(err)
		}
	}
	c := &Class{Name: "Logic", Color: "blue"}
	if err := s.CreateClass(c); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	a1 := &Assignment{ClassID: c.ID, Title: "Later", DueDate: "2026-11-02", PublishAt: now - 10, CreatedBy: mentor.ID}
	a2 := &Assignment{ClassID: c.ID, Title: "Sooner", DueDate: "2026-11-01", DueTime: "09:00", PublishAt: now - 10}
	a3 := &Assignment{ClassID: c.ID, Title: "Draft"}
	a4 := &Assignment{ClassID: c.ID, Title: "Undated", PublishAt: now - 10}
	for _, a := range []*Assignment{a1, a2, a3, a4} {
		if err := s.CreateAssignment(a); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListAssignments(AssignmentQuery{ClassIDs: []int64{c.ID}, PublishedOnly: true, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, a := range list {
		titles = append(titles, a.Title)
	}
	if strings.Join(titles, ",") != "Sooner,Later,Undated" || list[0].ClassName != "Logic" {
		t.Fatalf("published, by due date: %v", titles)
	}
	if list, _ := s.ListAssignments(AssignmentQuery{ClassIDs: []int64{c.ID}, DueFrom: "2026-11-02", DueTo: "2026-11-30"}); len(list) != 1 || list[0].ID != a1.ID {
		t.Fatalf("due range: %v", list)
	}

	// Work moves draft → turned in → needs work → turned in → complete.
	w, err := s.StartSubmission(a1.ID, sam.ID)
	if err != nil || w.Status != WorkDraft {
		t.Fatal(w, err)
	}
	if again, _ := s.StartSubmission(a1.ID, sam.ID); again.ID != w.ID {
		t.Fatal("starting twice must return the same work")
	}
	if err := s.SaveWork(w.ID, "first"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkStatus(w.ID, []string{WorkDraft}, WorkTurnedIn, sam.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWork(w.ID, "changed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("saving turned-in work: %v", err)
	}
	if err := s.SetWorkStatus(w.ID, []string{WorkDraft}, WorkTurnedIn, sam.ID, nil); !errors.Is(err, ErrWrongStatus) {
		t.Fatalf("turning in twice: %v", err)
	}
	note := "More, please."
	if err := s.SetWorkStatus(w.ID, []string{WorkTurnedIn}, WorkNeedsWork, mentor.ID, &note); err != nil {
		t.Fatal(err)
	}
	counts, _ := s.StatusCounts([]int64{a1.ID, a2.ID})
	if counts[a1.ID][WorkNeedsWork] != 1 || len(counts[a2.ID]) != 0 {
		t.Fatalf("counts: %v", counts)
	}
	w, _ = s.GetSubmission(a1.ID, sam.ID)
	if w.Feedback != note || w.FeedbackBy != mentor.ID || w.TurnedInAt == 0 || !w.Editable() {
		t.Fatalf("after feedback: %+v", w)
	}
	hist, _ := s.SubmissionHistory(w.ID)
	if len(hist) != 2 || hist[0].Kind != WorkTurnedIn || hist[1].By != "Mia" || hist[1].Note != note {
		t.Fatalf("history: %+v", hist)
	}

	// Files: deleting the assignment leaves its files' records to clean up.
	f1 := &File{OwnerKind: FileForAssignment, OwnerID: a1.ID, Name: "a.pdf", Size: 10, ContentType: "application/pdf", Stored: "aa/a1"}
	f2 := &File{OwnerKind: FileForSubmission, OwnerID: w.ID, Name: "b.txt", Size: 5, ContentType: "text/plain", Stored: "bb/b1"}
	f3 := &File{OwnerKind: FileForAssignment, OwnerID: a2.ID, Name: "c.txt", Size: 7, ContentType: "text/plain", Stored: "cc/c1"}
	for _, f := range []*File{f1, f2, f3} {
		if err := s.AddFile(f); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.FilesSize(); n != 22 {
		t.Fatalf("files size %d", n)
	}
	if err := s.DeleteAssignment(a1.ID); err != nil {
		t.Fatal(err)
	}
	paths, err := s.OrphanFiles()
	if err != nil || strings.Join(paths, ",") != "aa/a1,bb/b1" {
		t.Fatalf("orphans: %v %v", paths, err)
	}
	if left, _ := s.ListFiles(FileForAssignment, a2.ID); len(left) != 1 {
		t.Fatal("other files must stay")
	}
	if _, err := s.GetSubmissionByID(w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("work goes with its assignment")
	}
}

func TestChat(t *testing.T) {
	s := openTest(t)
	mk := func(username, role string) *User {
		u := &User{Username: username, DisplayName: strings.ToUpper(username[:1]) + username[1:], Role: role, PasswordHash: "x", Active: true}
		if err := s.CreateUser(u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	mia, sam := mk("mia", RoleMentor), mk("sam", RoleScholar)
	com, err := s.CommunityChannel()
	if err != nil || com.ClassID != 0 || !com.Enabled {
		t.Fatalf("community: %+v %v", com, err)
	}
	c := &Class{Name: "Logic", Color: "red"}
	s.CreateClass(c)
	if have, _ := s.ClassChannels([]int64{c.ID}); len(have) != 0 {
		t.Fatal("class channels are made when first needed")
	}
	ch, err := s.ClassChannel(c.ID)
	if err != nil || ch.ClassName != "Logic" || ch.ClassColor != "red" {
		t.Fatalf("class channel: %+v %v", ch, err)
	}
	if again, _ := s.ClassChannel(c.ID); again.ID != ch.ID {
		t.Fatal("one channel per class")
	}

	var ids []int64
	for i := 0; i < 5; i++ {
		m, err := s.PostMessage(com.ID, sam.ID, fmt.Sprintf("hello %d", i))
		if err != nil || m.AuthorName != "Sam" || m.AuthorRole != RoleScholar {
			t.Fatal(m, err)
		}
		ids = append(ids, m.ID)
	}
	page, more, _ := s.ListMessages(com.ID, 0, 3)
	if !more || len(page) != 3 || page[0].Body != "hello 2" || page[2].Body != "hello 4" {
		t.Fatalf("newest page: %v %v", page, more)
	}
	page, more, _ = s.ListMessages(com.ID, page[0].ID, 3)
	if more || len(page) != 2 || page[0].Body != "hello 0" {
		t.Fatalf("earlier page: %v %v", page, more)
	}
	if after, _ := s.MessagesAfter(com.ID, ids[2], 10); len(after) != 2 {
		t.Fatalf("after: %v", after)
	}

	// Unread: others' messages after what you've read, not removed.
	if n, _ := s.UnreadCounts(mia.ID, []int64{com.ID, ch.ID}); n[com.ID] != 5 || n[ch.ID] != 0 {
		t.Fatalf("unread: %v", n)
	}
	if n, _ := s.UnreadCounts(sam.ID, []int64{com.ID}); n[com.ID] != 0 {
		t.Fatal("your own messages aren't unread")
	}
	s.MarkRead(mia.ID, com.ID, ids[2])
	s.MarkRead(mia.ID, com.ID, ids[0]) // never goes backwards
	if err := s.DeleteMessage(ids[4], mia.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UnreadCounts(mia.ID, []int64{com.ID}); n[com.ID] != 1 {
		t.Fatalf("unread after reading and a removal: %v", n)
	}
	m, _ := s.GetMessage(ids[4])
	if !m.Deleted() || m.Body != "" || m.DeletedBy != mia.ID {
		t.Fatalf("removed: %+v", m)
	}
	if err := s.DeleteMessage(ids[4], mia.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("removing twice")
	}
	latest, _ := s.LatestMessages([]int64{com.ID, ch.ID})
	if latest[com.ID].ID != ids[4] || latest[ch.ID] != nil {
		t.Fatalf("latest: %v", latest)
	}

	// Mutes, with and without an end.
	if mu, _ := s.MuteOf(ch.ID, sam.ID); mu != nil {
		t.Fatal("not muted yet")
	}
	s.SetMute(ch.ID, sam.ID, 0, mia.ID)
	if mu, _ := s.MuteOf(ch.ID, sam.ID); mu == nil || mu.By != "Mia" || mu.Name != "Sam" {
		t.Fatalf("mute: %+v", mu)
	}
	s.SetMute(ch.ID, sam.ID, time.Now().Unix()-10, mia.ID)
	if mu, _ := s.MuteOf(ch.ID, sam.ID); mu != nil {
		t.Fatal("an expired mute isn't in force")
	}
	s.SetMute(ch.ID, sam.ID, time.Now().Unix()+3600, mia.ID)
	if list, _ := s.ListMutes(ch.ID); len(list) != 1 {
		t.Fatalf("mutes: %v", list)
	}
	s.Unmute(ch.ID, sam.ID)
	if list, _ := s.ListMutes(ch.ID); len(list) != 0 {
		t.Fatal("unmuted")
	}

	// Deleting a class takes its chat with it.
	s.PostMessage(ch.ID, mia.ID, "class message")
	if err := s.DeleteClass(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChannel(ch.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("class channel should go with the class")
	}
}

// TestBoardMigration checks that rebuilding users and files for migration 9
// keeps everything, and that links between tables still work afterwards.
func TestBoardMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := openTo(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	ann := &User{Username: "ann", DisplayName: "Ann", Role: RoleAdmin, PasswordHash: "h1", Active: true, Email: "a@x.org"}
	sam := &User{Username: "sam", DisplayName: "Sam", Role: RoleScholar, PasswordHash: "h2", Active: true}
	for _, u := range []*User{ann, sam} {
		if err := s.CreateUser(u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE users SET totp_secret = 'sealed', totp_enabled = 1, totp_last_step = 42 WHERE id = ?`, ann.ID); err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateSession(sam.ID, false, "ua", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	c := &Class{Name: "Logic", Color: "blue"}
	s.CreateClass(c)
	s.AddMembers(c.ID, ClassScholar, []int64{sam.ID})
	a := &Assignment{ClassID: c.ID, Title: "Essay", PublishAt: 1, CreatedBy: ann.ID}
	s.CreateAssignment(a)
	if err := s.AddFile(&File{OwnerKind: FileForAssignment, OwnerID: a.ID, Name: "a.pdf", Size: 3, ContentType: "application/pdf",
		Stored: "ab/abc", UploadedBy: ann.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO channels (class_id, enabled, created_at) VALUES (?, 1, 1)`, c.ID); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(); v < 9 {
		t.Fatalf("schema %d", v)
	}
	got, err := s.GetUser(ann.ID)
	if err != nil || got.Email != "a@x.org" || got.TOTPSecret != "sealed" || !got.TOTPEnabled || got.TOTPLastStep != 42 || got.PasswordHash != "h1" {
		t.Fatalf("user after migration: %+v %v", got, err)
	}
	if _, u, err := s.LookupSession(tok); err != nil || u.ID != sam.ID {
		t.Fatalf("session after migration: %v", err)
	}
	if files, _ := s.ListFiles(FileForAssignment, a.ID); len(files) != 1 || files[0].UploadedBy != ann.ID {
		t.Fatalf("files after migration: %v", files)
	}
	if com, err := s.CommunityChannel(); err != nil || com.Kind != ChannelCommunity {
		t.Fatalf("community: %+v %v", com, err)
	}
	if ch, err := s.ClassChannel(c.ID); err != nil || ch.Kind != ChannelClass {
		t.Fatalf("class channel: %+v %v", ch, err)
	}

	// The new role and file kind are allowed; others still aren't.
	bo := &User{Username: "bo", DisplayName: "Bo", Role: RoleBoard, PasswordHash: "x", Active: true}
	if err := s.CreateUser(bo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE users SET role = 'king' WHERE id = ?`, bo.ID); err == nil {
		t.Fatal("unknown roles must still be refused")
	}
	if err := s.AddFile(&File{OwnerKind: FileForMessage, OwnerID: 1, Name: "p.png", Size: 1, ContentType: "image/png", Stored: "cd/cde"}); err != nil {
		t.Fatal(err)
	}

	// Links still work: removing a user takes their sessions and class places.
	if _, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, sam.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.LookupSession(tok); !errors.Is(err, ErrNotFound) {
		t.Fatal("sessions should go with their user")
	}
	if role, _ := s.ClassRole(c.ID, sam.ID); role != "" {
		t.Fatal("class places should go with their user")
	}
	var fk int
	if err := s.db.QueryRow(`SELECT foreign_keys FROM pragma_foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign keys should be back on: %d %v", fk, err)
	}
}

func TestGroups(t *testing.T) {
	s := openTest(t)
	var ids []int64
	for _, n := range []string{"ann", "bo", "cy"} {
		u := &User{Username: n, DisplayName: strings.ToUpper(n), Role: RoleScholar, PasswordHash: "x", Active: true}
		s.CreateUser(u)
		ids = append(ids, u.ID)
	}
	g, err := s.CreateGroup("Parents' council", ids[0], ids[:2])
	if err != nil || g.Kind != ChannelGroup || g.Name != "Parents' council" || g.CreatedBy != ids[0] {
		t.Fatalf("group: %+v %v", g, err)
	}
	if in, _ := s.InChannel(g.ID, ids[1]); !in {
		t.Fatal("member")
	}
	if in, _ := s.InChannel(g.ID, ids[2]); in {
		t.Fatal("not a member")
	}
	if n, _ := s.AddChannelMembers(g.ID, ids); n != 1 {
		t.Fatalf("added %d", n)
	}
	s.RemoveChannelMember(g.ID, ids[0])
	if list, _ := s.ChannelMembers(g.ID); len(list) != 2 || list[0].Username != "bo" {
		t.Fatalf("members: %v", list)
	}
	if mine, _ := s.ListGroups(ids[0]); len(mine) != 0 {
		t.Fatal("removed members don't see the group")
	}
	if all, _ := s.ListGroups(0); len(all) != 1 {
		t.Fatal("all groups")
	}
	s.RenameChannel(g.ID, "Council")
	m, _ := s.PostMessage(g.ID, ids[1], "hi")
	s.AddFile(&File{OwnerKind: FileForMessage, OwnerID: m.ID, Name: "p.png", Size: 1, ContentType: "image/png", Stored: "pp/png"})
	if byMsg, _ := s.FilesFor(FileForMessage, []int64{m.ID}); len(byMsg[m.ID]) != 1 {
		t.Fatal("files for message")
	}
	if err := s.DeleteChannel(g.ID); err != nil {
		t.Fatal(err)
	}
	if paths, _ := s.OrphanFiles(); len(paths) != 1 || paths[0] != "pp/png" {
		t.Fatalf("a deleted group's files are left to clean up: %v", paths)
	}
	if com, _ := s.CommunityChannel(); s.DeleteChannel(com.ID) != nil {
		t.Fatal("delete")
	} else if _, err := s.GetChannel(com.ID); err != nil {
		t.Fatal("only group chats can be deleted")
	}
}
