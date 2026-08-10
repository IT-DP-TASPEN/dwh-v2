package web

import "embed"

// Files contains the production templates and generated frontend assets.
//
//go:embed all:templates static
var Files embed.FS
