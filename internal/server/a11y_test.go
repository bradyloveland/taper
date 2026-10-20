package server

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/bradyloveland/taper/internal/store"
)

// The accessibility check signs in as each kind of person, follows every
// link on the site, and checks each page against the parts of WCAG 2.2 AA
// that can be checked in the HTML: names for every control, labels for every
// field, alt text, heading order, unique IDs, working references between
// elements, a language and a main landmark. Colour contrast is checked
// separately, from the stylesheet.

// node is an element (or, with Tag "", a piece of text) in a parsed page.
type node struct {
	Tag      string
	Attr     map[string]string
	Text     string
	Children []*node
	Parent   *node
}

func (n *node) attr(k string) (string, bool) { v, ok := n.Attr[k]; return v, ok }

// parseHTML builds a tree from HTML with the XML reader's lenient mode.
func parseHTML(t *testing.T, src string) *node {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(src))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	root := &node{Tag: "#root", Attr: map[string]string{}}
	cur := root
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parsing HTML: %v", err)
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			n := &node{Tag: strings.ToLower(tk.Name.Local), Attr: map[string]string{}, Parent: cur}
			for _, a := range tk.Attr {
				name := strings.ToLower(a.Name.Local)
				if a.Name.Space != "" && a.Name.Space != "xmlns" {
					name = strings.ToLower(a.Name.Space) + ":" + name
				}
				n.Attr[name] = a.Value
			}
			cur.Children = append(cur.Children, n)
			cur = n
		case xml.EndElement:
			name := strings.ToLower(tk.Name.Local)
			for c := cur; c != nil && c != root; c = c.Parent {
				if c.Tag == name {
					cur = c.Parent
					break
				}
			}
		case xml.CharData:
			cur.Children = append(cur.Children, &node{Text: string(tk), Parent: cur})
		}
	}
	return root
}

func (n *node) walk(f func(*node)) {
	f(n)
	for _, c := range n.Children {
		c.walk(f)
	}
}

// hidden reports whether n or an ancestor is hidden from everyone, or from
// screen readers.
func (n *node) hidden() bool {
	for c := n; c != nil; c = c.Parent {
		if _, ok := c.Attr["hidden"]; ok {
			return true
		}
		if c.Attr["aria-hidden"] == "true" {
			return true
		}
	}
	return false
}

// text is n's text as a screen reader reads it: text, and images' alt text,
// skipping parts hidden from screen readers.
func (n *node) text() string {
	var b strings.Builder
	var rec func(*node)
	rec = func(c *node) {
		if c.Tag == "" {
			b.WriteString(c.Text)
			return
		}
		if c.Attr["aria-hidden"] == "true" || c.Tag == "script" || c.Tag == "style" {
			return
		}
		if c.Tag == "img" {
			b.WriteString(" " + c.Attr["alt"] + " ")
		}
		if c.Tag == "svg" {
			for _, k := range c.Children {
				if k.Tag == "title" {
					b.WriteString(" " + k.text() + " ")
				}
			}
			return
		}
		for _, k := range c.Children {
			rec(k)
		}
	}
	rec(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// name is the accessible name of a control.
func (n *node) name(byID map[string]*node) string {
	if v := strings.TrimSpace(n.Attr["aria-label"]); v != "" {
		return v
	}
	if ids := n.Attr["aria-labelledby"]; ids != "" {
		var parts []string
		for _, id := range strings.Fields(ids) {
			if l := byID[id]; l != nil {
				parts = append(parts, l.text())
			}
		}
		if s := strings.TrimSpace(strings.Join(parts, " ")); s != "" {
			return s
		}
	}
	if n.Tag == "input" {
		switch n.Attr["type"] {
		case "submit", "button", "reset":
			if v := n.Attr["value"]; v != "" {
				return v
			}
		}
	}
	if s := n.text(); s != "" {
		return s
	}
	return strings.TrimSpace(n.Attr["title"])
}

// checkPage returns what's wrong with a page.
func checkPage(t *testing.T, src string) []string {
	t.Helper()
	root := parseHTML(t, src)
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	byID := map[string]*node{}
	labelFor := map[string]bool{}
	var htmlEl, mainEl *node
	h1 := 0
	lastLevel := 0
	root.walk(func(n *node) {
		if n.Tag == "" {
			return
		}
		if id, ok := n.attr("id"); ok {
			if byID[id] != nil {
				add("the id %q is used twice", id)
			}
			byID[id] = n
		}
		if n.Tag == "label" {
			if f := n.Attr["for"]; f != "" {
				labelFor[f] = true
			}
		}
		switch n.Tag {
		case "html":
			htmlEl = n
		case "main":
			mainEl = n
		}
	})
	if htmlEl == nil || strings.TrimSpace(htmlEl.Attr["lang"]) == "" {
		add("the page has no language (<html lang>)")
	}
	if mainEl == nil {
		add("the page has no <main>")
	}
	root.walk(func(n *node) {
		if n.Tag == "" || n.Tag == "#root" || n.hidden() {
			return
		}
		desc := describe(n)
		if lv, err := strconv.Atoi(strings.TrimPrefix(n.Tag, "h")); err == nil && len(n.Tag) == 2 && n.Tag[0] == 'h' {
			if lv == 1 {
				h1++
			}
			if lastLevel > 0 && lv > lastLevel+1 {
				add("heading %s %q skips a level after h%d", n.Tag, n.text(), lastLevel)
			}
			if n.text() == "" {
				add("empty heading %s", n.Tag)
			}
			lastLevel = lv
		}
		for _, a := range []string{"aria-describedby", "aria-labelledby", "aria-controls"} {
			for _, id := range strings.Fields(n.Attr[a]) {
				if byID[id] == nil {
					add("%s points %s at a missing id %q", desc, a, id)
				}
			}
		}
		if ti, ok := n.attr("tabindex"); ok {
			if v, _ := strconv.Atoi(ti); v > 0 {
				add("%s has a positive tabindex", desc)
			}
		}
		switch n.Tag {
		case "img":
			if _, ok := n.attr("alt"); !ok {
				add("%s has no alt text", desc)
			}
		case "a":
			href, ok := n.attr("href")
			if !ok {
				return
			}
			if n.name(byID) == "" {
				add("link to %s has no name", href)
			}
			if strings.HasPrefix(href, "#") && len(href) > 1 && byID[href[1:]] == nil {
				add("link to %s: there's no such id on the page", href)
			}
		case "button", "summary":
			if n.name(byID) == "" {
				add("%s has no name", desc)
			}
		case "input", "select", "textarea":
			typ := n.Attr["type"]
			if n.Tag == "input" && (typ == "hidden" || typ == "submit" || typ == "button" || typ == "reset" || typ == "image") {
				if (typ == "submit" || typ == "button") && n.name(byID) == "" {
					add("%s has no name", desc)
				}
				return
			}
			if _, hiddenAttr := n.attr("hidden"); hiddenAttr {
				return // hidden helpers, like a username for password managers
			}
			labelled := n.Attr["aria-label"] != "" || n.Attr["aria-labelledby"] != "" || n.Attr["title"] != "" || labelFor[n.Attr["id"]]
			for p := n.Parent; p != nil && !labelled; p = p.Parent {
				labelled = p.Tag == "label"
			}
			if !labelled {
				add("%s has no label", desc)
			}
		}
	})
	if h1 != 1 {
		add("the page has %d <h1> headings, not one", h1)
	}
	return problems
}

func describe(n *node) string {
	s := "<" + n.Tag
	for _, k := range []string{"id", "name", "type", "class", "href"} {
		if v, ok := n.Attr[k]; ok {
			s += " " + k + "=" + strconv.Quote(v)
		}
	}
	return s + ">"
}

// crawl visits every page reachable by links from start, as b, and returns
// the problems found on each.
func crawl(t *testing.T, b *browser, start []string, limit int) (map[string][]string, int) {
	t.Helper()
	seen := map[string]bool{}
	perPath := map[string]int{}
	queue := append([]string{}, start...)
	found := map[string][]string{}
	pages := 0
	for len(queue) > 0 && pages < limit {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		u, err := url.Parse(p)
		if err != nil {
			continue
		}
		if perPath[u.Path]++; perPath[u.Path] > 3 {
			continue // calendar dates and the like: a few are enough
		}
		r := b.get(p)
		if r.Status != http.StatusOK || !strings.Contains(r.Header.Get("Content-Type"), "text/html") {
			continue
		}
		pages++
		if probs := checkPage(t, r.Body); len(probs) > 0 {
			found[p] = probs
		}
		root := parseHTML(t, r.Body)
		root.walk(func(n *node) {
			if n.Tag != "a" {
				return
			}
			href := n.Attr["href"]
			if i := strings.Index(href, "#"); i >= 0 {
				href = href[:i]
			}
			if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
				return
			}
			for _, skip := range []string{"/logout", "/files/", "/static/", "/ical/", "/admin/backup", "/notifications/"} {
				if strings.HasPrefix(href, skip) {
					return
				}
			}
			if strings.Contains(href, "download") {
				return
			}
			if !seen[href] {
				queue = append(queue, href)
			}
		})
	}
	return found, pages
}

func TestAccessibility(t *testing.T) {
	c := newChatEnv(t)
	pam := c.addUser("pam", "Pam Parent", store.RoleParent, "parent-password", false)
	c.store.LinkParent(pam.ID, c.sam.ID)
	c.store.AddMembers(c.class.ID, store.ClassScholar, []int64{c.zoe.ID})
	m := c.signedIn("mia", "mentor-password")
	classPath := fmt.Sprintf("/classes/%d", c.class.ID)
	id := assignmentID(t, m.postFiles(classPath+"/assignments/new", url.Values{"title": {"Essay"}, "instructions": {"Write **one** page."},
		"due_date": {c.tomorrow}, "publish": {"now"}, "work_online": {"1"}, "allow_files": {"1"}}, map[string]string{"notes.txt": "notes"}))
	s := c.signedIn("sam", "scholar-password")
	s.postFiles("/assignments/"+id+"/work", url.Values{"body": {"My essay."}, "action": {"turn_in"}}, map[string]string{"essay.txt": "x"})
	eventID(t, m.postForm("/events/new", url.Values{"calendar": {fmt.Sprint(c.class.ID)}, "title": {"Field trip"},
		"start_date": {c.tomorrow}, "all_day": {"1"}}))
	s.postJSON(c.cls, url.Values{"body": {"Hello class"}})
	a := c.signedIn("admin", "admin-password")
	a.postForm("/chat/new", url.Values{"name": {"Choir"}, "member": {fmt.Sprint(c.sam.ID), fmt.Sprint(c.mia.ID)}})

	people := []struct {
		name string
		b    *browser
		more []string
	}{
		{"admin", a, []string{"/admin/people/new", "/admin/people/import", "/classes/new", "/events/new?cal=school", "/chat/new",
			"/account/two-step", "/account/passkeys", fmt.Sprintf("/admin/people/%d", pam.ID)}},
		{"mentor", m, []string{classPath + "/assignments/new", "/assignments/" + id + "/edit", fmt.Sprintf("/assignments/%s/work/%d", id, c.sam.ID),
			classPath + "/members", classPath + "/edit"}},
		{"scholar", s, []string{"/account/two-step"}},
		{"parent", c.signedIn("pam", "parent-password"), nil},
		{"signed out", c.browser(), []string{"/login", "/forgot", "/offline", "/no-such-page"}},
	}
	total := 0
	var report []string
	for _, p := range people {
		found, pages := crawl(t, p.b, append([]string{"/"}, p.more...), 400)
		total += pages
		paths := make([]string, 0, len(found))
		for k := range found {
			paths = append(paths, k)
		}
		sort.Strings(paths)
		for _, path := range paths {
			for _, prob := range found[path] {
				report = append(report, p.name+" "+path+": "+prob)
			}
		}
	}
	if total < 60 {
		t.Fatalf("only %d pages checked; the crawl isn't reaching the site", total)
	}
	t.Logf("checked %d pages", total)
	if len(report) > 0 {
		t.Errorf("%d accessibility problems:\n%s", len(report), strings.Join(report, "\n"))
	}
}

func TestCheckPage(t *testing.T) {
	good := `<!doctype html><html lang="en"><body><main><h1>Hi</h1><h2>A</h2>
	<label for="x">Name</label><input id="x" name="x"><label><input type="checkbox" name="c"> Yes</label>
	<button type="submit">Go</button><a href="/">Home</a><img src="a.png" alt=""><input type="hidden" name="csrf">
	<a href="/x"><span class="sr-only">Close</span><svg aria-hidden="true"></svg></a></main></body></html>`
	if p := checkPage(t, good); len(p) > 0 {
		t.Fatalf("good page: %q", p)
	}
	bad := `<html><body><h1>One</h1><h3>Skipped</h3><h1>Two</h1><input name="q"><button><svg></svg></button>
	<a href="/y"></a><img src="b.png"><p id="d"></p><p id="d"></p><a href="#nowhere">x</a><span aria-describedby="gone">z</span></body></html>`
	p := checkPage(t, bad)
	for _, want := range []string{"no language", "no <main>", "skips a level", "2 <h1>", "has no label", "<button> has no name",
		"link to /y has no name", "no alt text", "used twice", "no such id", "missing id"} {
		found := false
		for _, got := range p {
			found = found || strings.Contains(got, want)
		}
		if !found {
			t.Errorf("missing %q in %q", want, p)
		}
	}
}
