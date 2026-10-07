package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bradyloveland/taper/internal/auth"
	"github.com/bradyloveland/taper/internal/store"
)

// Uploaded files live under DataDir/files, with random names; their real
// names are in the database.
const (
	maxFile      = 25 << 20  // one file
	maxFiles     = 20        // on one assignment or piece of work
	maxFilesBody = 105 << 20 // one request with files
)

func (s *Server) filesDir() string { return filepath.Join(s.cfg.DataDir, "files") }

// errUpload is a problem with an upload to tell the person about.
type errUpload struct{ msg string }

func (e errUpload) Error() string { return e.msg }

// saveUploads stores the files in the form field "files" for an owner.
// have is how many files the owner has already.
func (s *Server) saveUploads(r *http.Request, kind string, ownerID int64, have int, by int64) (int, error) {
	if r.MultipartForm == nil {
		return 0, nil
	}
	var list []*multipart.FileHeader
	for _, fh := range r.MultipartForm.File["files"] {
		if fh.Filename != "" && fh.Size > 0 {
			list = append(list, fh)
		}
	}
	if len(list) == 0 {
		return 0, nil
	}
	if have+len(list) > maxFiles {
		return 0, errUpload{fmt.Sprintf("You can attach up to %d files. Remove some first.", maxFiles)}
	}
	for _, fh := range list {
		if fh.Size > maxFile {
			return 0, errUpload{fmt.Sprintf("%s is too big. Files can be up to %d MB.", cleanFileName(fh.Filename), maxFile>>20)}
		}
	}
	n := 0
	for _, fh := range list {
		if err := s.saveUpload(fh, kind, ownerID, by); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (s *Server) saveUpload(fh *multipart.FileHeader, kind string, ownerID, by int64) error {
	src, err := fh.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	tok := strings.ToLower(auth.Token(16))
	rel := path.Join(tok[:2], tok)
	full := filepath.Join(s.filesDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return err
	}
	dst, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	size, err := io.Copy(dst, io.LimitReader(src, maxFile+1))
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil && size > maxFile {
		err = errUpload{cleanFileName(fh.Filename) + " is too big."}
	}
	if err != nil {
		os.Remove(full)
		return err
	}
	name := cleanFileName(fh.Filename)
	ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	f := &store.File{OwnerKind: kind, OwnerID: ownerID, Name: name, Size: size, ContentType: ctype, Stored: rel, UploadedBy: by}
	if err := s.store.AddFile(f); err != nil {
		os.Remove(full)
		return err
	}
	return nil
}

// cleanFileName keeps the last part of an uploaded file's name, without
// characters that cause trouble in downloads.
func cleanFileName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 || strings.ContainsRune(`/"<>|:*?`, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	for utf8.RuneCountInString(name) > 120 {
		ext := filepath.Ext(name)
		runes := []rune(strings.TrimSuffix(name, ext))
		if len(ext) > 20 || len(runes) == 0 {
			name = string([]rune(name)[:120])
			break
		}
		name = string(runes[:len(runes)-1]) + ext
	}
	return name
}

// removeStored deletes stored files from disk.
func (s *Server) removeStored(paths ...string) {
	for _, p := range paths {
		if p == "" || strings.Contains(p, "..") {
			continue
		}
		if err := os.Remove(filepath.Join(s.filesDir(), filepath.FromSlash(p))); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("removing a file", "file", p, "err", err)
		}
	}
}

// cleanFiles removes files whose assignment or work was deleted.
func (s *Server) cleanFiles() {
	paths, err := s.store.OrphanFiles()
	if err != nil {
		slog.Error("finding unused files", "err", err)
		return
	}
	s.removeStored(paths...)
}

// uploadError explains a failed upload, or reports a server error. It
// returns the message to show.
func (s *Server) uploadError(r *http.Request, err error) string {
	var u errUpload
	if errors.As(err, &u) {
		return u.msg
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return fmt.Sprintf("That's too much to upload at once. Files can be up to %d MB each.", maxFile>>20)
	}
	s.logError(r, "saving upload", err)
	return "The file couldn't be saved. Try again, and report a problem if it keeps happening."
}

// parseUpload reads a form that may have files. A body that's too big is
// reported as an upload error.
func parseUpload(r *http.Request) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return r.ParseForm()
	}
	if r.MultipartForm != nil {
		return nil
	}
	return r.ParseMultipartForm(8 << 20)
}

// inlineTypes are shown in the browser; everything else downloads.
var inlineTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"application/pdf": true, "text/plain; charset=utf-8": true}

// handleFile serves /files/{id}/{name} to people who may see it.
func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	f, err := s.store.GetFile(id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.logError(r, "loading file", err)
		}
		s.notFound(w, r)
		return
	}
	if !s.canSeeFile(current(r).user, f) {
		s.notFound(w, r)
		return
	}
	fh, err := os.Open(filepath.Join(s.filesDir(), filepath.FromSlash(f.Stored)))
	if err != nil {
		s.serverError(w, r, "opening file", err)
		return
	}
	defer fh.Close()
	disp := "attachment"
	ctype := f.ContentType
	if ctype == "text/plain" {
		ctype = "text/plain; charset=utf-8"
	}
	if inlineTypes[ctype] && r.URL.Query().Get("download") == "" {
		disp = "inline"
	} else {
		ctype = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", disp+"; filename*=UTF-8''"+url.PathEscape(f.Name))
	if ctype != "application/pdf" { // browsers' PDF viewers don't run in a sandbox
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; style-src 'unsafe-inline'")
	}
	h.Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", fileTime(fh), fh)
}

// canSeeFile checks that u may download f.
func (s *Server) canSeeFile(u *store.User, f *store.File) bool {
	switch f.OwnerKind {
	case store.FileForAssignment:
		a, err := s.store.GetAssignment(f.OwnerID)
		if err != nil {
			return false
		}
		acc, err := s.assignmentAccess(u, a)
		return err == nil && acc.CanSee
	case store.FileForSubmission:
		wk, err := s.store.GetSubmissionByID(f.OwnerID)
		if err != nil {
			return false
		}
		if wk.ScholarID == u.ID {
			return true
		}
		a, err := s.store.GetAssignment(wk.AssignmentID)
		if err != nil {
			return false
		}
		acc, err := s.assignmentAccess(u, a)
		return err == nil && acc.CanReview
	}
	return false
}

func fileTime(f *os.File) time.Time {
	if st, err := f.Stat(); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// fileLink is a file's address.
func fileLink(f *store.File) string {
	return "/files/" + strconv.FormatInt(f.ID, 10) + "/" + url.PathEscape(f.Name)
}
