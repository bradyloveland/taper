package server

import (
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bradyloveland/taper/internal/store"
)

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,39}$`)

// formValues returns the trimmed values of the named form fields.
func formValues(r *http.Request, names ...string) map[string]string {
	m := make(map[string]string, len(names))
	for _, n := range names {
		m[n] = strings.TrimSpace(r.PostFormValue(n))
	}
	return m
}

// cleanName checks a name typed into a form. what is used in the message.
func cleanName(v, what string) (string, string) {
	v = strings.Join(strings.Fields(v), " ")
	if v == "" {
		return "", "Enter a " + what + "."
	}
	if utf8.RuneCountInString(v) > 100 {
		return "", "That " + what + " is too long. Use 100 characters or fewer."
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return "", "That " + what + " has characters that can't be used."
		}
	}
	return v, ""
}

// cleanUsername lower-cases and checks a username.
func cleanUsername(v string) (string, string) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "", "Enter a username."
	}
	if !usernameRE.MatchString(v) {
		return "", "Usernames are 2 to 40 letters, numbers, dots, dashes or underscores, starting with a letter or number."
	}
	return v, ""
}

func cleanEmail(v string) (string, string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ""
	}
	a, err := mail.ParseAddress(v)
	if err != nil || a.Address != v || len(v) > 200 {
		return "", "That email address doesn't look right. Check it, or leave it empty."
	}
	return v, ""
}

// validateProfile fills u's name, username and email from a form, returning
// a message if something is wrong.
func validateProfile(u *store.User, form map[string]string) string {
	var msg string
	if u.DisplayName, msg = cleanName(form["display_name"], "name"); msg != "" {
		return msg
	}
	if u.Username, msg = cleanUsername(form["username"]); msg != "" {
		return msg
	}
	if u.Email, msg = cleanEmail(form["email"]); msg != "" {
		return msg
	}
	return ""
}
