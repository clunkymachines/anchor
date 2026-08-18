// Package static provides Anchor's embedded browser assets.
package static

import "embed"

// Files contains every stylesheet, script, image, and bundled license served
// by the web application.
//
//go:embed *.css *.js *.png *.LICENSE fonts/*
var Files embed.FS
