// Package ui holds the HTML templates and static assets, embedded into the binary.
package ui

import "embed"

// Files contains the html/ templates and static/ assets.
//
//go:embed "html" "static"
var Files embed.FS
