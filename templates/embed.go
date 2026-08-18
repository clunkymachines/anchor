// Package templates provides Anchor's embedded HTML templates.
package templates

import "embed"

// Files contains every HTML template used by the web application.
//
//go:embed *.html
var Files embed.FS
