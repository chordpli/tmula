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
