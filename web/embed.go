// Package web embeds the page templates and static files.
package web

import "embed"

// Templates holds templates/*.html.
//
//go:embed templates/*.html
var Templates embed.FS

// Static holds static/: styles, scripts, icons and the service worker.
//
//go:embed static
var Static embed.FS
