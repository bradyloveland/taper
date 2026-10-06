package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	h, err := hashWith("correct horse", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse", h) {
		t.Fatal("password should verify")
	}
	if VerifyPassword("wrong horse", h) {
		t.Fatal("wrong password verified")
	}
	h2, _ := hashWith("correct horse", 1000)
	if h == h2 {
		t.Fatal("hashes should be salted")
	}
	for _, bad := range []string{"", "plain", "pbkdf2_sha256$x$y$z", "pbkdf2_sha256$99999999999$AA==$AA==", "md5$1$AA==$AA=="} {
		if VerifyPassword("x", bad) {
			t.Errorf("malformed hash %q verified", bad)
		}
	}
	if !strings.HasPrefix(dummyHash, "pbkdf2_sha256$600000$") {
		t.Fatalf("default hash uses wrong parameters: %s", dummyHash)
	}
}

func TestCheckPassword(t *testing.T) {
	if CheckPassword("short") == "" {
		t.Fatal("short password accepted")
	}
	if CheckPassword("long enough") != "" {
		t.Fatal("good password refused")
	}
	if CheckPassword(strings.Repeat("a", 300)) == "" {
		t.Fatal("huge password accepted")
	}
}

func TestCodes(t *testing.T) {
	c := Code(2)
	if len(c) != 9 || c[4] != '-' || strings.ContainsAny(c, "01OI") {
		t.Fatalf("bad code %q", c)
	}
	if !SameCode(c, strings.ToLower(strings.ReplaceAll(c, "-", " "))) {
		t.Fatal("codes should compare loosely")
	}
	if SameCode(c, "") || SameCode("", "") || SameCode(c, Code(2)) {
		t.Fatal("different codes matched")
	}
	p := TempPassword()
	if CheckPassword(p) != "" || strings.Count(p, "-") != 2 {
		t.Fatalf("bad temp password %q", p)
	}
	if a, b := Token(32), Token(32); a == b || len(a) != 43 {
		t.Fatal("tokens should be random and 43 chars")
	}
	if HashToken("a") == HashToken("b") || len(HashToken("a")) != 64 {
		t.Fatal("bad token hash")
	}
}

func TestThrottle(t *testing.T) {
	now := time.Unix(1000, 0)
	th := NewThrottle(3, time.Minute)
	th.Now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if th.Blocked("Ann") != 0 {
			t.Fatalf("blocked after %d failures", i)
		}
		th.Fail("ann")
		now = now.Add(10 * time.Second)
	}
	if d := th.Blocked("ANN"); d != 30*time.Second {
		t.Fatalf("blocked for %v, want 30s", d)
	}
	if th.Blocked("bob") != 0 {
		t.Fatal("other keys shouldn't be blocked")
	}
	now = now.Add(30 * time.Second)
	if th.Blocked("ann") != 0 {
		t.Fatal("should be unblocked once the oldest failure expires")
	}
	th.Fail("ann")
	th.Reset("Ann")
	if th.Blocked("ann") != 0 || len(th.fails) != 0 {
		t.Fatal("reset should clear failures")
	}
}
