//go:build embed_frontend

package main

import (
	"embed"
	"io/fs"
	"log"
)

// Use the `all:` prefix so files starting with `_` or `.` are included.
// Vite emits chunks like `_plugin-vue_export-helper-<hash>.js`; without
// `all:` those silently disappear from the embedded FS and the SPA fails
// to load at runtime.
//
//go:embed all:dist
var embeddedFrontend embed.FS

func frontendFS() fs.FS {
	sub, err := fs.Sub(embeddedFrontend, "dist")
	if err != nil {
		log.Fatalf("frontend fs: %v", err)
	}
	return sub
}
