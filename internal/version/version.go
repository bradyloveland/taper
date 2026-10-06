// Package version holds the application version, read from the VERSION file
// next to this source so releases can check the tag against one place.
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// override replaces VERSION at build time (-X), for tests of updates.
var override string

// Version is the running version, for example "0.1.0" or "0.2.0-dev".
var Version = pick()

func pick() string {
	if override != "" {
		return override
	}
	return strings.TrimSpace(raw)
}

// Name is the product name.
const Name = "Taper"

// Repo is the GitHub repository, as owner/name.
const Repo = "bradyloveland/taper"
