// Package docs embeds the user guide so the app can show the same pages that
// are on GitHub.
package docs

import "embed"

// Guide holds guide/*.md. guide/README.md is the table of contents.
//
//go:embed guide/*.md
var Guide embed.FS
