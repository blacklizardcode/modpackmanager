// Package version holds the application version.
//
// The version lives in the VERSION file next to this source so that Go builds,
// the Nix package, the packaging scripts and the release workflow all read the
// same value. To release: edit VERSION, commit, tag "v<VERSION>", push the tag.
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// String returns the version, for example "0.1.0".
func String() string { return strings.TrimSpace(raw) }
