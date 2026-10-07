// Package markdown turns Markdown into safe HTML. Raw HTML in the source is
// dropped and dangerous link schemes (javascript: and the like) are removed,
// so it's safe for text written by users as well as for the guide.
package markdown

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// LinkFunc rewrites a link destination. Return it unchanged to keep it.
type LinkFunc func(dest string) string

// Options adjust rendering.
type Options struct {
	Links      LinkFunc // rewrites link and image destinations
	HeadingIDs bool     // add id attributes to headings (for the guide)
	HardWraps  bool     // keep single line breaks, for writing that isn't Markdown-aware
}

// Render converts src to HTML.
func Render(src []byte, opt Options) (template.HTML, error) {
	popts := []parser.Option{}
	if opt.HeadingIDs {
		popts = append(popts, parser.WithAutoHeadingID())
	}
	var ropts []renderer.Option
	if opt.HardWraps {
		ropts = append(ropts, html.WithHardWraps())
	}
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(popts...),
		goldmark.WithRendererOptions(ropts...),
	)
	doc := md.Parser().Parse(text.NewReader(src))
	if opt.Links != nil {
		_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if !entering {
				return ast.WalkContinue, nil
			}
			switch l := n.(type) {
			case *ast.Link:
				l.Destination = []byte(opt.Links(string(l.Destination)))
			case *ast.Image:
				l.Destination = []byte(opt.Links(string(l.Destination)))
			}
			return ast.WalkContinue, nil
		})
	}
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil // goldmark escapes text and drops raw HTML by default
}

// Title returns the text of the first level-one heading, or "".
func Title(src []byte) string {
	doc := goldmark.New().Parser().Parse(text.NewReader(src))
	var title string
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && entering && h.Level == 1 {
			title = plain(h, src)
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return title
}

func plain(n ast.Node, src []byte) string {
	var b bytes.Buffer
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := c.(*ast.Text); ok && entering {
			b.Write(t.Segment.Value(src))
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}
