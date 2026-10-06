// Package github files issues in a GitHub repository, for Taper's
// "Report a problem" form.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultAPI is GitHub's REST API.
const DefaultAPI = "https://api.github.com"

// Client talks to GitHub with a token that may create issues.
type Client struct {
	API       string // DefaultAPI by default
	Token     string
	UserAgent string
	HTTP      *http.Client
}

var repoRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)

// ValidRepo reports whether s looks like "owner/name".
func ValidRepo(s string) bool { return repoRE.MatchString(s) }

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	api := c.API
	if api == "" {
		api = DefaultAPI
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, api+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return errors.New("couldn't reach GitHub. Check that this server can reach the internet")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("GitHub didn't accept the token. It may have expired or been revoked; an admin can add a new one under Settings")
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		return errors.New("the token isn't allowed to create issues in that repository. Give it the Issues (read and write) permission for the repository")
	case resp.StatusCode == http.StatusGone:
		return errors.New("issues are turned off for that repository")
	case resp.StatusCode >= 300:
		var e struct{ Message string }
		_ = json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		return fmt.Errorf("GitHub refused the request: %s", e.Message)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return errors.New("GitHub sent an unexpected reply")
		}
	}
	return nil
}

// CheckRepo confirms the token can see repo.
func (c *Client) CheckRepo(ctx context.Context, repo string) error {
	var r struct {
		HasIssues bool `json:"has_issues"`
	}
	if err := c.do(ctx, "GET", "/repos/"+repo, nil, &r); err != nil {
		return err
	}
	if !r.HasIssues {
		return errors.New("issues are turned off for that repository")
	}
	return nil
}

// Issue is a created issue.
type Issue struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
}

// CreateIssue opens an issue in repo.
func (c *Client) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (*Issue, error) {
	var is Issue
	in := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		in["labels"] = labels
	}
	if err := c.do(ctx, "POST", "/repos/"+repo+"/issues", in, &is); err != nil {
		return nil, err
	}
	if is.URL == "" {
		return nil, errors.New("GitHub sent an unexpected reply")
	}
	return &is, nil
}

// maxURL keeps pre-filled issue links within what browsers and GitHub accept.
const maxURL = 7000

// NewIssueURL is a link that opens GitHub's new-issue form, pre-filled, for
// someone signed in to GitHub to submit.
func NewIssueURL(repo, title, body string, labels []string) string {
	q := url.Values{"title": {title}}
	if len(labels) > 0 {
		q.Set("labels", strings.Join(labels, ","))
	}
	base := "https://github.com/" + repo + "/issues/new?"
	for {
		q.Set("body", body)
		u := base + q.Encode()
		if len(u) <= maxURL || body == "" {
			return u
		}
		cut := len(body) - (len(u) - maxURL) - 40
		if cut < 0 {
			cut = 0
		}
		body = strings.ToValidUTF8(body[:cut], "") + "\n\n(cut short)"
	}
}
