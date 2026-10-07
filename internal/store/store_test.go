package store

import (
	"errors"
	"path/filepath"
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
