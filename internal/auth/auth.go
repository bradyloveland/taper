// Package auth holds sign-in primitives: password hashing, random tokens,
// temporary passwords and brute-force throttling.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Iterations is the PBKDF2-SHA256 work factor for new hashes (OWASP 2023+).
// Tests lower it to run quickly.
var Iterations = 600_000

const maxIterations = 10_000_000

// MinPasswordLength is the shortest password accepted.
const MinPasswordLength = 8

// HashPassword returns "pbkdf2_sha256$<iterations>$<salt>$<hash>".
func HashPassword(password string) (string, error) { return hashWith(password, Iterations) }

func hashWith(password string, iterations int) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", iterations,
		base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(dk)), nil
}

// VerifyPassword reports whether password matches a stored hash. Malformed
// hashes never match.
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > maxIterations {
		return false
	}
	salt, err1 := base64.StdEncoding.DecodeString(parts[2])
	expected, err2 := base64.StdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(expected) == 0 {
		return false
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(dk, expected) == 1
}

// dummyHash is checked when a username doesn't exist, so a sign-in takes as
// long whether or not the account exists.
var dummyHash, _ = HashPassword("not a real password")

// BurnTime does the work of one password check.
func BurnTime(password string) { VerifyPassword(password, dummyHash) }

// CheckPassword returns a message saying what's wrong with a new password, or "".
func CheckPassword(password string) string {
	if len([]rune(password)) < MinPasswordLength {
		return fmt.Sprintf("Use at least %d characters for the password.", MinPasswordLength)
	}
	if len(password) > 256 {
		return "That password is too long. Use 256 characters or fewer."
	}
	return ""
}

// Token returns a random URL-safe token with n bytes of randomness.
func Token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand doesn't fail on Linux
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken is how session tokens are stored, so a copy of the database
// doesn't hold anything that signs someone in.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// codeAlphabet leaves out letters and digits that are easy to mix up.
const codeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Code returns a random code of groups of four characters, like "K7QF-2MXP".
func Code(groups int) string {
	var b strings.Builder
	buf := make([]byte, groups*4)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	for i, c := range buf {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(codeAlphabet[int(c)%len(codeAlphabet)])
	}
	return b.String()
}

// TempPassword returns a temporary password that's easy to read out or copy
// from a screen, like "house-K7QF-2MXP".
func TempPassword() string {
	words := []string{"maple", "river", "cedar", "stone", "meadow", "harbor", "aspen", "willow", "canyon", "summit", "prairie", "orchard"}
	b := make([]byte, 1)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return words[int(b[0])%len(words)] + "-" + strings.ToLower(Code(2))
}

// SameCode compares two codes ignoring case, spaces and dashes, in constant time.
func SameCode(a, b string) bool {
	norm := func(s string) []byte {
		s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
		return []byte(s)
	}
	x, y := norm(a), norm(b)
	return len(x) > 0 && subtle.ConstantTimeCompare(x, y) == 1
}
