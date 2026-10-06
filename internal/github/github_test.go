package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCreateIssue(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r":
			w.Write([]byte(`{"has_issues":true}`))
		case r.Method == "GET" && r.URL.Path == "/repos/o/noissues":
			w.Write([]byte(`{"has_issues":false}`))
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/issues":
			json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"number":12,"html_url":"https://github.com/o/r/issues/12"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &Client{API: srv.URL, Token: "good"}
	ctx := context.Background()
	if err := c.CheckRepo(ctx, "o/r"); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckRepo(ctx, "o/noissues"); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("no issues: %v", err)
	}
	if err := c.CheckRepo(ctx, "o/missing"); err == nil || !strings.Contains(err.Error(), "isn't allowed") {
		t.Fatalf("missing repo: %v", err)
	}
	is, err := c.CreateIssue(ctx, "o/r", "Title", "Body", []string{"bug"})
	if err != nil || is.Number != 12 {
		t.Fatalf("create: %+v %v", is, err)
	}
	if got["title"] != "Title" || got["body"] != "Body" {
		t.Fatalf("sent %v", got)
	}
	bad := &Client{API: srv.URL, Token: "bad"}
	if _, err := bad.CreateIssue(ctx, "o/r", "T", "B", nil); err == nil || !strings.Contains(err.Error(), "didn't accept the token") {
		t.Fatalf("bad token: %v", err)
	}
	off := &Client{API: "http://127.0.0.1:1", Token: "x"}
	if err := off.CheckRepo(ctx, "o/r"); err == nil || !strings.Contains(err.Error(), "couldn't reach") {
		t.Fatalf("offline: %v", err)
	}
}

func TestNewIssueURL(t *testing.T) {
	u := NewIssueURL("o/r", "Broken & bad", "Line 1\nLine 2", []string{"bug"})
	p, err := url.Parse(u)
	if err != nil || p.Host != "github.com" || p.Path != "/o/r/issues/new" {
		t.Fatalf("url %s", u)
	}
	q := p.Query()
	if q.Get("title") != "Broken & bad" || q.Get("body") != "Line 1\nLine 2" || q.Get("labels") != "bug" {
		t.Fatalf("query %v", q)
	}
	long := NewIssueURL("o/r", "T", strings.Repeat("é long text ", 2000), nil)
	if len(long) > maxURL || !strings.Contains(long, "cut+short") {
		t.Fatalf("long url: %d", len(long))
	}
	if !ValidRepo("bradyloveland/taper") || ValidRepo("nope") || ValidRepo("a/b/c") || ValidRepo("../x") {
		t.Fatal("ValidRepo")
	}
}
