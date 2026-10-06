package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"sort"
	"strings"
)

// assets are the embedded static files, each with a short content hash so
// pages can link to them with a long cache lifetime.
type assets struct {
	files  map[string][]byte // path under static/: content
	hashes map[string]string // path: short hash
	all    string            // hash of every file, for the service worker's cache name
}

func loadAssets(root fs.FS) (*assets, error) {
	a := &assets{files: map[string][]byte{}, hashes: map[string]string{}}
	sub := mustFS(root, "static")
	all := sha256.New()
	var names []string
	err := fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		names = append(names, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	for _, p := range names {
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		a.files[p] = b
		a.hashes[p] = hex.EncodeToString(sum[:])[:10]
		all.Write([]byte(p))
		all.Write(sum[:])
	}
	a.all = hex.EncodeToString(all.Sum(nil))[:10]
	return a, nil
}

// url is the cache-busting address of a static file.
func (a *assets) url(p string) string {
	h, ok := a.hashes[p]
	if !ok {
		panic("unknown static file " + p)
	}
	return "/static/" + p + "?v=" + h
}

// precache lists what the service worker keeps for offline use.
func (a *assets) precache() []string {
	return []string{a.url("app.css"), a.url("app.js"), a.url("icons/icon.svg"), a.url("icons/icon-192.png")}
}

func contentType(p string) string {
	if t := mime.TypeByExtension(path.Ext(p)); t != "" {
		return t
	}
	return "application/octet-stream"
}

func (a *assets) serve(w http.ResponseWriter, r *http.Request, p string, immutable bool) {
	b, ok := a.files[p]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType(p))
	if immutable {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	w.Header().Set("ETag", `"`+a.hashes[p]+`"`)
	if r.Header.Get("If-None-Match") == `"`+a.hashes[p]+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(b)
}

// handler serves /static/. A request carrying the current hash may be cached
// for good.
func (a *assets) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/static/")
		if p == "sw.js" { // only served from the root, where its scope is the whole site
			http.NotFound(w, r)
			return
		}
		a.serve(w, r, p, r.URL.Query().Get("v") == a.hashes[p])
	})
}

// file serves one static file at a fixed address.
func (a *assets) file(p string) http.HandlerFunc {
	if _, ok := a.files[p]; !ok {
		panic("unknown static file " + p)
	}
	return func(w http.ResponseWriter, r *http.Request) { a.serve(w, r, p, false) }
}
