package server

import (
	"bytes"
	"compress/gzip"
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
	gzips  map[string][]byte // compressed copies of text files, for browsers that accept gzip
	hashes map[string]string // path: short hash
	all    string            // hash of every file, for the service worker's cache name
}

func loadAssets(root fs.FS) (*assets, error) {
	a := &assets{files: map[string][]byte{}, gzips: map[string][]byte{}, hashes: map[string]string{}}
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
		if compressible(p) {
			var z bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&z, gzip.BestCompression)
			zw.Write(b)
			zw.Close()
			if z.Len() < len(b) {
				a.gzips[p] = z.Bytes()
			}
		}
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

// compressible reports whether a static file is text worth compressing.
// Only static files are compressed: pages carry a CSRF token alongside what
// people type, which compression could leak (BREACH).
func compressible(p string) bool {
	switch path.Ext(p) {
	case ".css", ".js", ".svg", ".json", ".txt", ".webmanifest":
		return true
	}
	return false
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
	z, zipped := a.gzips[p]
	etag := `"` + a.hashes[p] + `"`
	if zipped {
		w.Header().Set("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			etag = `"` + a.hashes[p] + `-gz"`
		}
	}
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if zipped && acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		b = z
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

// acceptsGzip reports whether the browser takes gzip (every current one does).
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") && strings.ReplaceAll(strings.TrimSpace(q), " ", "") != "q=0" {
			return true
		}
	}
	return false
}
