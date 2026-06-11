// Package web embeds and serves the built React control-plane UI so the whole
// tool ships as a single binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// staticFS holds the UI assets. A placeholder index.html is committed so the
// binary builds without a prior `make web-build`; a real build overwrites it.
//
//go:embed all:static
var staticFS embed.FS

// FS returns the embedded UI file system rooted at the static directory.
func FS() (fs.FS, error) {
	return fs.Sub(staticFS, "static")
}

// Handler serves the embedded UI as static files.
func Handler() http.Handler {
	sub, err := FS()
	if err != nil {
		// The "static" directory is always present at build time.
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}

// HasBuiltUI reports whether the embedded UI is a real `make web` build rather
// than the committed placeholder page, so callers (e.g. `tmula demo`) can warn
// that the live console is missing from this binary.
func HasBuiltUI() bool {
	return hasBuiltUI(staticFS)
}

// hasBuiltUI checks fsys for a non-empty static/assets directory: the bundler
// always emits one and `make embed` copies it in, while the placeholder commit
// ships only static/index.html.
func hasBuiltUI(fsys fs.FS) bool {
	entries, err := fs.ReadDir(fsys, "static/assets")
	return err == nil && len(entries) > 0
}
