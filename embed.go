package main

import "embed"

// staticFS holds the compiled web UI (static-src/ → static/, populated
// by tsgo at build time). Isolated in its own file so editors and
// linters scanning main.go don't block on the //go:embed directive
// (which requires the static/ directory to exist) during a cold
// clone before `make static` has run. Consumed via
// fs.Sub(staticFS, "static") in main.go.

//go:embed static
var staticFS embed.FS
