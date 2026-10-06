package server

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/bradyloveland/taper/internal/markdown"
	"github.com/bradyloveland/taper/internal/update"
	"github.com/bradyloveland/taper/internal/version"
)

// maxUpload is the largest release file accepted.
const maxUpload = 200 << 20

// settingCheckDaily turns the daily check for new versions on or off.
const settingCheckDaily = "updates_check_daily"

// Restarting is closed when the server should stop so systemd starts the
// version that was just installed (or put back).
func (s *Server) Restarting() <-chan struct{} { return s.restart }

// RollbackRequested is the reason, if the restart is to go back to the
// previous version. That's done once the database is closed.
func (s *Server) RollbackRequested() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rollback
}

// UpdateFiles is where the program and data are, for rolling back.
func (s *Server) UpdateFiles() update.Files { return s.updater.Files }

// Started is called once the server is listening. A version that was just
// installed counts as working once it has stayed up for settle.
func (s *Server) Started(settle time.Duration) { s.updater.Started(settle) }

// Stop cancels background work on shutdown.
func (s *Server) Stop() { s.updater.Stop() }

func (s *Server) requestRestart(rollback string) {
	s.mu.Lock()
	s.rollback = rollback
	s.mu.Unlock()
	// Give the browser its answer before stopping.
	time.AfterFunc(500*time.Millisecond, func() { s.restartOnce.Do(func() { close(s.restart) }) })
}

func (s *Server) checkDaily() bool {
	on := true
	_, _ = s.store.GetSetting(settingCheckDaily, &on)
	return on
}

// Run does background work until ctx ends: the daily check for updates.
func (s *Server) Run(ctx context.Context) {
	first := time.After(2 * time.Minute)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first:
		case <-t.C:
		}
		if !s.checkDaily() {
			continue
		}
		if c := s.updater.LastCheck(); time.Since(time.Unix(c.Checked, 0)) >= 24*time.Hour {
			c = s.updater.CheckNow(ctx)
			if c.Error != "" {
				slog.Warn("checking for updates", "err", c.Error)
			}
		}
	}
}

// availableUpdate returns the newer version found by the last check, or "".
func (s *Server) availableUpdate() string {
	if c := s.updater.LastCheck(); c.Latest != nil && s.updater.Newer(c.Latest.Version) {
		return c.Latest.Version
	}
	return ""
}

type updatesData struct {
	Version     string
	Arch        string
	CantUpdate  string
	Check       update.Check
	Newer       bool
	Notes       template.HTML
	Staged      *update.Staged
	StagedNotes template.HTML
	StagedNewer bool
	RollbackTo  string
	Last        *update.State
	CheckDaily  bool
	Error       string
}

func notesHTML(notes string) template.HTML {
	if notes == "" {
		return ""
	}
	h, err := markdown.Render([]byte(notes), markdown.Options{})
	if err != nil {
		return ""
	}
	return h
}

func (s *Server) renderUpdates(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	d := updatesData{Version: version.Version, Arch: runtime.GOARCH, CantUpdate: s.updater.CantUpdate(),
		Check: s.updater.LastCheck(), RollbackTo: s.updater.CanRollBack(), CheckDaily: s.checkDaily(), Error: errMsg}
	if d.Check.Latest != nil {
		d.Newer = s.updater.Newer(d.Check.Latest.Version)
		d.Notes = notesHTML(d.Check.Latest.Notes)
	}
	if st := s.updater.StagedRelease(); st != nil {
		d.Staged, d.StagedNotes, d.StagedNewer = st, notesHTML(st.Notes), s.updater.Newer(st.Version)
	}
	if last, err := update.ReadState(s.cfg.DataDir); err == nil && last != nil && !last.Seen {
		d.Last = last
	}
	s.render(w, r, status, "updates", "Updates", "updates", d)
}

func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	s.renderUpdates(w, r, http.StatusOK, "")
}

// updateError turns an updater error into a message for the page.
func updateError(err error) string {
	var in *update.InputError
	if errors.As(err, &in) {
		return in.Message
	}
	return capitalize(err.Error()) + "."
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	c := s.updater.CheckNow(r.Context())
	if c.Error != "" {
		s.renderUpdates(w, r, http.StatusBadGateway, c.Error)
		return
	}
	msg := "You have the newest version."
	if c.Latest != nil && s.updater.Newer(c.Latest.Version) {
		msg = "Taper " + c.Latest.Version + " is available."
	}
	s.redirect(w, r, "/admin/updates", msg)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if err := s.store.SetSetting(settingCheckDaily, r.PostFormValue("check_daily") == "1"); err != nil {
		s.serverError(w, r, "saving update settings", err)
		return
	}
	s.redirect(w, r, "/admin/updates", "Saved.")
}

// handleUpdateDownload fetches the newest release from GitHub and stages it.
func (s *Server) handleUpdateDownload(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if _, err := s.updater.Download(ctx, r.PostFormValue("version")); err != nil {
		s.renderUpdates(w, r, http.StatusBadGateway, updateError(err))
		return
	}
	s.redirect(w, r, "/admin/updates", "The release is downloaded and checked. Install it when you're ready.")
}

func (s *Server) handleUpdateUpload(w http.ResponseWriter, r *http.Request) {
	f, _, err := r.FormFile("release")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.renderUpdates(w, r, http.StatusRequestEntityTooLarge, "That file is too large to be a Taper release.")
			return
		}
		s.renderUpdates(w, r, http.StatusUnprocessableEntity, "Choose a release file (taper-<version>-linux-"+runtime.GOARCH+".tar.gz) to upload.")
		return
	}
	defer f.Close()
	if _, err := s.updater.Upload(f); err != nil {
		s.renderUpdates(w, r, http.StatusUnprocessableEntity, updateError(err))
		return
	}
	s.redirect(w, r, "/admin/updates", "The release file is checked. Install it when you're ready.")
}

type restartingData struct {
	Heading string
	Target  string
}

func (s *Server) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	to, err := s.updater.Install()
	if err != nil {
		s.renderUpdates(w, r, http.StatusUnprocessableEntity, updateError(err))
		return
	}
	slog.Info("installing update", "by", current(r).user.Username, "version", to)
	s.requestRestart("")
	s.render(w, r, http.StatusOK, "restarting", "Updating", "updates", restartingData{Heading: "Installing Taper " + to, Target: to})
}

func (s *Server) handleUpdateDiscard(w http.ResponseWriter, r *http.Request) {
	s.updater.Discard()
	s.redirect(w, r, "/admin/updates", "The downloaded release is removed.")
}

func (s *Server) handleUpdateRollback(w http.ResponseWriter, r *http.Request) {
	to := s.updater.CanRollBack()
	if to == "" {
		s.renderUpdates(w, r, http.StatusUnprocessableEntity, "There's no earlier version to go back to.")
		return
	}
	if why := s.updater.CantUpdate(); why != "" {
		s.renderUpdates(w, r, http.StatusUnprocessableEntity, why)
		return
	}
	slog.Info("rolling back", "by", current(r).user.Username, "to", to)
	s.requestRestart("Rolled back by hand from the Updates page.")
	s.render(w, r, http.StatusOK, "restarting", "Going back", "updates", restartingData{Heading: "Going back to Taper " + to, Target: to})
}

func (s *Server) handleUpdateDismiss(w http.ResponseWriter, r *http.Request) {
	if err := s.updater.Dismiss(); err != nil {
		s.serverError(w, r, "dismissing update result", err)
		return
	}
	http.Redirect(w, r, "/admin/updates", http.StatusSeeOther)
}

// handleBackupDownload sends a fresh copy of the database.
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(s.cfg.DataDir, "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.serverError(w, r, "creating backups folder", err)
		return
	}
	tmp, err := os.CreateTemp(dir, "download-*.db")
	if err != nil {
		s.serverError(w, r, "creating backup file", err)
		return
	}
	path := tmp.Name()
	tmp.Close()
	os.Remove(path) // VACUUM INTO needs a file that doesn't exist yet
	defer os.Remove(path)
	if err := s.store.Backup(path); err != nil {
		s.serverError(w, r, "backing up database", err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.serverError(w, r, "opening backup", err)
		return
	}
	defer f.Close()
	name := "taper-" + time.Now().Format("2006-01-02-1504") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	slog.Info("database backup downloaded", "by", current(r).user.Username)
	http.ServeContent(w, r, name, time.Now(), f)
}
