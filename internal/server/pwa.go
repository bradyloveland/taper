package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bradyloveland/taper/internal/version"
)

// Colors shared by the manifest and the page's theme-color.
const (
	themeColor      = "#1f3a5f"
	backgroundColor = "#f7f5f0"
)

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	name, short := s.appNames()
	icon := func(p, size, purpose, typ string) map[string]string {
		return map[string]string{"src": s.assets.url(p), "sizes": size, "type": typ, "purpose": purpose}
	}
	m := map[string]any{
		"id":               "/",
		"name":             name,
		"short_name":       short,
		"description":      "Classes, calendars, assignments and chat for " + name + ".",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"orientation":      "any",
		"theme_color":      themeColor,
		"background_color": backgroundColor,
		// PNG only: iOS can't use an SVG home-screen icon and shows a
		// letter instead, if it picks one from the manifest.
		"icons": []map[string]string{
			icon("icons/apple-touch-icon.png", "180x180", "any", "image/png"),
			icon("icons/icon-192.png", "192x192", "any", "image/png"),
			icon("icons/icon-512.png", "512x512", "any", "image/png"),
			icon("icons/icon-maskable-512.png", "512x512", "maskable", "image/png"),
		},
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	_ = json.NewEncoder(w).Encode(m)
}

// handleServiceWorker serves sw.js from the site root (so it covers every
// page), with the current asset list filled in. Its content changes whenever
// a static file does, which makes browsers install the new worker.
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	list, _ := json.Marshal(s.assets.precache())
	body := bytes.NewBufferString(strings.NewReplacer(
		"__CACHE__", "taper-"+version.Version+"-"+s.assets.all,
		"__PRECACHE__", string(list),
	).Replace(string(s.assets.files["sw.js"])))
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = body.WriteTo(w)
}

// handleOffline is the page the service worker shows when there's no
// connection. It has no personal data, so it's safe to cache.
func (s *Server) handleOffline(w http.ResponseWriter, r *http.Request) {
	name, _ := s.appNames()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := s.pages["offline"].ExecuteTemplate(w, "offline", map[string]string{"AppName": name}); err != nil {
		s.logError(r, "rendering offline page", err)
	}
}
