package markdown

import (
	"strings"
	"testing"
)

func TestRenderSafe(t *testing.T) {
	src := "# Hello *there*\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1)) [ok](other.md#part)\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	out, err := Render([]byte(src), Options{HeadingIDs: true, Links: func(d string) string {
		if strings.HasPrefix(d, "other.md") {
			return "/guide/other" + strings.TrimPrefix(d, "other.md")
		}
		return d
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"<script", "javascript:"} {
		if strings.Contains(s, bad) {
			t.Errorf("output contains %q: %s", bad, s)
		}
	}
	for _, want := range []string{`<h1 id="hello-there">`, `href="/guide/other#part"`, "<table>"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q: %s", want, s)
		}
	}
	if got := Title([]byte(src)); got != "Hello there" {
		t.Fatalf("title %q", got)
	}
	if Title([]byte("no heading")) != "" {
		t.Fatal("title without heading")
	}
}
