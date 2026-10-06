package server

import (
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/bradyloveland/taper/internal/markdown"
	"github.com/bradyloveland/taper/internal/version"
)

// guidePage is one rendered page of the user guide.
type guidePage struct {
	Slug  string // "" for the contents page (README.md)
	Title string
	HTML  template.HTML
}

// guide is the user guide from docs/guide, rendered once at startup.
type guide struct {
	pages  []*guidePage // in the order README.md lists them, contents first
	bySlug map[string]*guidePage
}

var guideLinkRE = regexp.MustCompile(`\]\(([a-z0-9-]+)\.md(?:#[^)]*)?\)`)

func loadGuide(fsys fs.FS) (*guide, error) {
	g := &guide{bySlug: map[string]*guidePage{}}
	names, err := fs.Glob(fsys, "guide/*.md")
	if err != nil {
		return nil, err
	}
	readme, err := fs.ReadFile(fsys, "guide/README.md")
	if err != nil {
		return nil, err
	}
	// Order: README first, then as README links them, then any others.
	order := []string{"README"}
	seen := map[string]bool{"README": true}
	for _, m := range guideLinkRE.FindAllStringSubmatch(string(readme), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			order = append(order, m[1])
		}
	}
	var rest []string
	for _, n := range names {
		base := strings.TrimSuffix(path.Base(n), ".md")
		if !seen[base] {
			rest = append(rest, base)
		}
	}
	sort.Strings(rest)
	order = append(order, rest...)

	for _, base := range order {
		src, err := fs.ReadFile(fsys, "guide/"+base+".md")
		if err != nil {
			continue // linked from README but missing; tests catch this
		}
		html, err := markdown.Render(src, markdown.Options{HeadingIDs: true, Links: guideLink})
		if err != nil {
			return nil, err
		}
		slug := base
		if base == "README" {
			slug = ""
		}
		title := markdown.Title(src)
		if title == "" {
			title = base
		}
		p := &guidePage{Slug: slug, Title: title, HTML: html}
		g.pages = append(g.pages, p)
		g.bySlug[slug] = p
	}
	return g, nil
}

// guideLink points links between guide pages at the in-app pages, and links
// elsewhere in the repository at GitHub.
func guideLink(dest string) string {
	if strings.Contains(dest, "://") || strings.HasPrefix(dest, "#") || strings.HasPrefix(dest, "mailto:") {
		return dest
	}
	file, frag, _ := strings.Cut(dest, "#")
	if frag != "" {
		frag = "#" + frag
	}
	switch {
	case file == "README.md":
		return "/guide" + frag
	case strings.HasSuffix(file, ".md") && !strings.Contains(file, "/"):
		return "/guide/" + strings.TrimSuffix(file, ".md") + frag
	}
	// Anything else in the repository, relative to docs/guide.
	return "https://github.com/" + version.Repo + "/blob/main/" + path.Clean(path.Join("docs/guide", file)) + frag
}

type guideData struct {
	Pages []*guidePage
	Slug  string
	HTML  template.HTML
}

func (s *Server) handleGuide(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("page")
	if slug == "README" {
		http.Redirect(w, r, "/guide", http.StatusMovedPermanently)
		return
	}
	p, ok := s.guide.bySlug[slug]
	if !ok {
		s.notFound(w, r)
		return
	}
	title := p.Title
	if slug == "" {
		title = "Guide"
	}
	s.render(w, r, http.StatusOK, "guide", title, "guide", guideData{Pages: s.guide.pages[1:], Slug: slug, HTML: p.HTML})
}
