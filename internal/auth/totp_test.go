package auth

import (
	"encoding/base32"
	"regexp"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B, SHA-1, secret "12345678901234567890", 8 digits.
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := base32.StdEncoding.EncodeToString([]byte("12345678901234567890"))
	vectors := []struct {
		t    int64
		want string
	}{{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"},
		{1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"}}
	for _, v := range vectors {
		got, err := TOTPCode(secret, v.t/30, 8)
		if err != nil || got != v.want {
			t.Errorf("t=%d: got %s (%v), want %s", v.t, got, err, v.want)
		}
	}
}

func TestNewSecretIs160BitBase32(t *testing.T) {
	s := NewTOTPSecret()
	if !regexp.MustCompile(`^[A-Z2-7]{32}$`).MatchString(s) {
		t.Fatalf("bad secret %q", s)
	}
}

func at(step int64) time.Time { return time.Unix(step*30+5, 0) }

func code(t *testing.T, secret string, step int64) string {
	c, err := TOTPCode(secret, step, 6)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTOTPWindowAndReplay(t *testing.T) {
	s := NewTOTPSecret()
	for _, step := range []int64{999, 1000, 1001} {
		if got, ok := TOTPMatch(s, code(t, s, step), 0, at(1000)); !ok || got != step {
			t.Errorf("step %d should match", step)
		}
	}
	for _, step := range []int64{997, 998, 1002, 1003} {
		if _, ok := TOTPMatch(s, code(t, s, step), 0, at(1000)); ok {
			t.Errorf("step %d is outside the window", step)
		}
	}
	if _, ok := TOTPMatch(s, code(t, s, 1000), 1000, at(1000)); ok {
		t.Error("replay of a used step accepted")
	}
	if _, ok := TOTPMatch(s, code(t, s, 999), 1000, at(1000)); ok {
		t.Error("older step accepted after a newer one was used")
	}
	if got, ok := TOTPMatch(s, code(t, s, 1001), 1000, at(1000)); !ok || got != 1001 {
		t.Error("next step should still work")
	}
}

func TestTOTPToleratesSpacesAndRejectsJunk(t *testing.T) {
	s := NewTOTPSecret()
	c := code(t, s, 1000)
	if _, ok := TOTPMatch(s, c[:3]+" "+c[3:], 0, at(1000)); !ok {
		t.Error("spaces should be ignored")
	}
	for _, junk := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := TOTPMatch(s, junk, 0, at(1000)); ok {
			t.Errorf("%q accepted", junk)
		}
	}
	if _, ok := TOTPMatch("", "123456", 0, at(1000)); ok {
		t.Error("empty secret accepted")
	}
}

func TestTOTPURI(t *testing.T) {
	uri := TOTPURI("ABCDEFGH", "PBC Manager", "nas:admin")
	for _, want := range []string{"otpauth://totp/PBC%20Manager:nas:admin?", "secret=ABCDEFGH",
		"issuer=PBC%20Manager", "period=30", "digits=6"} {
		if !strings.Contains(uri, want) {
			t.Errorf("%s missing %q", uri, want)
		}
	}
	if GroupSecret("ABCDEFGHIJ") != "ABCD EFGH IJ" {
		t.Error("grouping")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes := NewRecoveryCodes(10)
	seen := map[string]bool{}
	re := regexp.MustCompile(`^[a-z2-9]{5}-[a-z2-9]{5}$`)
	for _, c := range codes {
		if !re.MatchString(c) || strings.ContainsAny(c, "01ilo") {
			t.Errorf("bad code %q", c)
		}
		seen[c] = true
	}
	if len(seen) != 10 {
		t.Fatal("codes should be unique")
	}
	if HashRecovery("abcde-fghjk") != HashRecovery("ABCDE FGHJK") || HashRecovery("abcde-fghjk") != HashRecovery("abcdefghjk") {
		t.Error("hash should ignore case and separators")
	}
	if HashRecovery("abcde-fghjk") == HashRecovery("abcde-fghjm") {
		t.Error("different codes hashed the same")
	}
}
