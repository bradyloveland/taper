package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bradyloveland/taper/internal/github"
	"github.com/bradyloveland/taper/internal/store"
	"github.com/bradyloveland/taper/internal/version"
)

// Settings for sending reports to GitHub. The token is stored encrypted.
const (
	settingReportRepo  = "reports_repo"
	settingReportToken = "reports_token"
)

// reportLabels are put on issues filed from Taper ("bug" exists in every
// new GitHub repository).
var reportLabels = []string{"bug"}

func (s *Server) reportRepo() string {
	repo := ""
	_, _ = s.store.GetSetting(settingReportRepo, &repo)
	if repo == "" {
		repo = version.Repo
	}
	return repo
}

// reportToken returns the saved GitHub token, or "".
func (s *Server) reportToken() string {
	var sealed string
	if ok, _ := s.store.GetSetting(settingReportToken, &sealed); !ok {
		return ""
	}
	tok, err := s.box.Open(sealed)
	if err != nil {
		slog.Error("reading the GitHub token", "err", err)
		return ""
	}
	return tok
}

func (s *Server) github(token string) *github.Client {
	return &github.Client{API: s.githubAPI, Token: token, UserAgent: "taper/" + version.Version}
}

type reportData struct {
	Title    string
	What     string
	Page     string
	Details  bool
	Error    string
	Sent     *store.BugReport
	Fallback string // pre-filled GitHub link, when it wasn't sent
}

func (s *Server) handleReportForm(w http.ResponseWriter, r *http.Request) {
	page := r.URL.Query().Get("from")
	if safeNext(page) != page {
		page = ""
	}
	s.render(w, r, http.StatusOK, "report", "Report a problem", "", reportData{Page: page, Details: true})
}

// noMentions stops text written in Taper from notifying people on GitHub.
func noMentions(s string) string { return strings.ReplaceAll(s, "@", "@\u200b") }

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	ri := current(r)
	d := reportData{
		Title:   strings.Join(strings.Fields(r.PostFormValue("title")), " "),
		What:    strings.TrimSpace(r.PostFormValue("what")),
		Page:    r.PostFormValue("page"),
		Details: r.PostFormValue("details") == "1",
	}
	if safeNext(d.Page) != d.Page {
		d.Page = ""
	}
	fail := func(status int, msg string) {
		d.Error = msg
		s.render(w, r, status, "report", "Report a problem", "", d)
	}
	switch {
	case d.Title == "":
		fail(http.StatusUnprocessableEntity, "Give the problem a short title.")
		return
	case utf8.RuneCountInString(d.Title) > 120:
		fail(http.StatusUnprocessableEntity, "Keep the title to 120 characters or fewer.")
		return
	case d.What == "":
		fail(http.StatusUnprocessableEntity, "Describe what happened.")
		return
	case utf8.RuneCountInString(d.What) > 5000:
		fail(http.StatusUnprocessableEntity, "That description is too long. Keep it to 5,000 characters or fewer.")
		return
	}
	key := strconv.FormatInt(ri.user.ID, 10)
	if wait := s.reportThrottle.Blocked(key); wait > 0 {
		fail(http.StatusTooManyRequests, "You've sent several reports in the last hour. Thank you! Please wait a while before sending another.")
		return
	}
	s.reportThrottle.Fail(key)

	var body strings.Builder
	body.WriteString(noMentions(d.What))
	body.WriteString("\n\n---\n")
	if d.Details {
		fmt.Fprintf(&body, "- Taper version: %s\n", version.Version)
		if d.Page != "" {
			fmt.Fprintf(&body, "- Page: `%s`\n", strings.ReplaceAll(d.Page, "`", ""))
		}
		fmt.Fprintf(&body, "- Browser: %s\n", device(r.UserAgent()))
		fmt.Fprintf(&body, "- Reported by: a %s\n", strings.ToLower(ri.user.RoleLabel()))
	}
	body.WriteString("\n_Reported from inside Taper._\n")
	rep := &store.BugReport{UserID: ri.user.ID, Title: noMentions(d.Title), Body: body.String(), Page: d.Page}
	if err := s.store.CreateBugReport(rep); err != nil {
		s.serverError(w, r, "saving report", err)
		return
	}
	slog.Info("problem reported", "by", ri.user.Username, "report", rep.ID)
	s.sendReport(r.Context(), rep)
	where := "It's saved in Taper under Settings → Problem reports."
	if rep.IssueURL != "" {
		where = "It's on GitHub: " + rep.IssueURL
	}
	s.notifyAdmins("Problem report: "+rep.Title, ri.user.DisplayName+" reported a problem in Taper.\n\n"+d.What+"\n\n"+where)
	d.Sent = rep
	if rep.IssueURL == "" {
		d.Fallback = github.NewIssueURL(s.reportRepo(), rep.Title, rep.Body, reportLabels)
	}
	s.render(w, r, http.StatusOK, "report", "Report a problem", "", d)
}

// sendReport files rep as a GitHub issue if a token is saved, recording
// the result on rep and in the database.
func (s *Server) sendReport(ctx context.Context, rep *store.BugReport) {
	token := s.reportToken()
	if token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	is, err := s.github(token).CreateIssue(ctx, s.reportRepo(), rep.Title, rep.Body, reportLabels)
	if err != nil {
		rep.Error = capitalize(err.Error()) + "."
		slog.Warn("sending report to GitHub", "report", rep.ID, "err", err)
	} else {
		rep.IssueURL, rep.IssueNumber, rep.Error = is.URL, is.Number, ""
	}
	if err := s.store.SetBugReportIssue(rep.ID, rep.IssueURL, rep.IssueNumber, rep.Error); err != nil {
		slog.Error("saving report result", "err", err)
	}
}

type reportsData struct {
	Reports  []*store.BugReport
	CanSend  bool
	Repo     string
	Fallback map[int64]string
}

func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListBugReports(200)
	if err != nil {
		s.serverError(w, r, "listing reports", err)
		return
	}
	d := reportsData{Reports: list, CanSend: s.reportToken() != "", Repo: s.reportRepo(), Fallback: map[int64]string{}}
	for _, rep := range list {
		if rep.IssueURL == "" {
			d.Fallback[rep.ID] = github.NewIssueURL(d.Repo, rep.Title, rep.Body, reportLabels)
		}
	}
	s.render(w, r, http.StatusOK, "reports", "Problem reports", "settings", d)
}

func (s *Server) handleReportSend(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	rep, err := s.store.GetBugReport(id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, "loading report", err)
		return
	}
	if rep.IssueURL != "" {
		s.redirect(w, r, "/admin/reports", "That report is already on GitHub.")
		return
	}
	if s.reportToken() == "" {
		s.redirect(w, r, "/admin/reports", "")
		return
	}
	s.sendReport(r.Context(), rep)
	if rep.IssueURL == "" {
		s.setFlash(w, r, "error", "It wasn't sent: "+rep.Error)
		http.Redirect(w, r, "/admin/reports", http.StatusSeeOther)
		return
	}
	s.redirect(w, r, "/admin/reports", fmt.Sprintf("Sent to GitHub as issue #%d.", rep.IssueNumber))
}

// handleReportSettingsSave saves where reports go and the token.
func (s *Server) handleReportSettingsSave(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.PostFormValue("repo"))
	if repo == "" {
		repo = version.Repo
	}
	token := strings.TrimSpace(r.PostFormValue("token"))
	remove := r.PostFormValue("remove_token") == "1"
	if !github.ValidRepo(repo) {
		s.renderSettingsWith(w, r, http.StatusUnprocessableEntity, settingsErrors{Reports: "The repository should look like owner/name, for example " + version.Repo + "."})
		return
	}
	check := token
	if check == "" && !remove {
		check = s.reportToken()
	}
	if check != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := s.github(check).CheckRepo(ctx, repo); err != nil {
			s.renderSettingsWith(w, r, http.StatusUnprocessableEntity, settingsErrors{Reports: "Not saved: " + capitalize(err.Error()) + "."})
			return
		}
	}
	if err := s.store.SetSetting(settingReportRepo, repo); err != nil {
		s.serverError(w, r, "saving report settings", err)
		return
	}
	switch {
	case remove:
		if err := s.store.SetSetting(settingReportToken, ""); err != nil {
			s.serverError(w, r, "removing token", err)
			return
		}
	case token != "":
		if err := s.store.SetSetting(settingReportToken, s.box.Seal(token)); err != nil {
			s.serverError(w, r, "saving token", err)
			return
		}
	}
	slog.Info("report settings saved", "by", current(r).user.Username, "repo", repo, "token_changed", token != "" || remove)
	s.redirect(w, r, "/admin/settings", "Problem report settings saved.")
}
