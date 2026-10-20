package server

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/web"
)

// Colour contrast (WCAG 2.2 AA): 4.5:1 for text, 3:1 for focus rings and
// other parts people need to see. The colours come from app.css, in the
// light and dark themes.

type cssRule struct {
	selector string
	decls    map[string]string
	dark     bool // inside @media (prefers-color-scheme: dark)
}

// parseCSS reads rules (with their @media context) from a stylesheet.
func parseCSS(src string) []cssRule {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
	var out []cssRule
	var stack []string // the selectors or at-rules we're inside
	start := 0
	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '{':
			stack = append(stack, strings.TrimSpace(src[start:i]))
			start = i + 1
		case '}':
			if len(stack) == 0 {
				start = i + 1
				continue
			}
			sel := stack[len(stack)-1]
			body := src[start:i]
			stack = stack[:len(stack)-1]
			start = i + 1
			if strings.HasPrefix(sel, "@") || strings.Contains(body, "{") {
				continue
			}
			r := cssRule{selector: sel, decls: map[string]string{}}
			for _, at := range stack {
				if strings.Contains(at, "prefers-color-scheme: dark") {
					r.dark = true
				}
			}
			for _, d := range strings.Split(body, ";") {
				k, v, ok := strings.Cut(d, ":")
				if ok {
					r.decls[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			out = append(out, r)
		}
	}
	return out
}

// themes returns the colour variables for light and dark.
func themes(rules []cssRule) (light, dark map[string]string) {
	light, dark = map[string]string{}, map[string]string{}
	for _, r := range rules {
		if r.selector != ":root" {
			continue
		}
		for k, v := range r.decls {
			if strings.HasPrefix(k, "--") {
				if r.dark {
					dark[k] = v
				} else {
					light[k] = v
				}
			}
		}
	}
	for k, v := range light {
		if _, ok := dark[k]; !ok {
			dark[k] = v
		}
	}
	return light, dark
}

var varRE = regexp.MustCompile(`var\((--[a-z0-9-]+)\)`)

// colour resolves a CSS colour to RGB (0–1), or reports false for anything
// it can't (gradients, transparency, currentColor).
func colour(v string, vars map[string]string) ([3]float64, bool) {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "!important"))
	for i := 0; i < 5; i++ {
		m := varRE.FindStringSubmatch(v)
		if m == nil || m[0] != v {
			break
		}
		v = vars[m[1]]
	}
	switch v {
	case "white", "#fff", "#ffffff":
		return [3]float64{1, 1, 1}, true
	case "black", "#000", "#000000":
		return [3]float64{}, true
	}
	if !strings.HasPrefix(v, "#") || strings.ContainsAny(v, " ") {
		return [3]float64{}, false
	}
	h := v[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return [3]float64{}, false
	}
	var c [3]float64
	for i := 0; i < 3; i++ {
		n, err := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
		if err != nil {
			return c, false
		}
		c[i] = float64(n) / 255
	}
	return c, true
}

func luminance(c [3]float64) float64 {
	lin := func(v float64) float64 {
		if v <= 0.04045 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c[0]) + 0.7152*lin(c[1]) + 0.0722*lin(c[2])
}

func ratio(a, b [3]float64) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func TestContrast(t *testing.T) {
	src, err := web.Static.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	rules := parseCSS(string(src))
	light, dark := themes(rules)
	if len(light) < 15 || len(dark) < 15 {
		t.Fatalf("colour variables not found: %d light, %d dark", len(light), len(dark))
	}
	topbar := map[bool]string{false: "var(--primary)", true: "#16243a"}
	var problems []string
	check := func(theme string, vars map[string]string, what, fg, bg string, min float64) {
		f, ok1 := colour(fg, vars)
		b, ok2 := colour(bg, vars)
		if !ok1 || !ok2 {
			problems = append(problems, fmt.Sprintf("%s: can't read the colours for %s (%s on %s)", theme, what, fg, bg))
			return
		}
		if r := ratio(f, b); r < min {
			problems = append(problems, fmt.Sprintf("%s: %s is %.2f:1 (%s on %s), needs %.1f:1", theme, what, r, fg, bg, min))
		}
	}
	for _, th := range []struct {
		name string
		vars map[string]string
		dark bool
	}{{"light", light, false}, {"dark", dark, true}} {
		for _, bg := range []string{"--bg", "--surface", "--surface-2"} {
			check(th.name, th.vars, "text on "+bg, "var(--text)", "var("+bg+")", 4.5)
			check(th.name, th.vars, "muted text on "+bg, "var(--muted)", "var("+bg+")", 4.5)
			check(th.name, th.vars, "links on "+bg, "var(--link)", "var("+bg+")", 4.5)
			check(th.name, th.vars, "the focus ring on "+bg, "var(--focus)", "var("+bg+")", 3)
		}
		check(th.name, th.vars, "button text", "var(--primary-text)", "var(--primary)", 4.5)
		check(th.name, th.vars, "button text on hover", "var(--primary-text)", "var(--primary-hover)", 4.5)
		check(th.name, th.vars, "messages: done", "var(--ok-text)", "var(--ok-bg)", 4.5)
		check(th.name, th.vars, "messages: errors", "var(--error-text)", "var(--error-bg)", 4.5)
		check(th.name, th.vars, "messages: warnings", "var(--warn-text)", "var(--warn-bg)", 4.5)
		check(th.name, th.vars, "the top bar's text", "#ffffff", topbar[th.dark], 4.5)
		check(th.name, th.vars, "the focus ring on the top bar", "var(--focus-bright)", topbar[th.dark], 3)

		// Every rule that sets both a text colour and a background.
		for _, r := range rules {
			if r.selector == ":root" || r.dark && !th.dark {
				continue
			}
			fg, ok1 := r.decls["color"]
			bg, ok2 := r.decls["background"]
			if !ok2 {
				bg, ok2 = r.decls["background-color"]
			}
			if !ok1 || !ok2 {
				continue
			}
			if _, ok := colour(fg, th.vars); !ok {
				continue
			}
			if _, ok := colour(bg, th.vars); !ok {
				continue
			}
			check(th.name, th.vars, r.selector, fg, bg, 4.5)
		}
	}
	if len(problems) > 0 {
		t.Errorf("%d contrast problems:\n%s", len(problems), strings.Join(problems, "\n"))
	}
}
