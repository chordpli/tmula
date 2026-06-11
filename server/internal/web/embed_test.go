package web

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestEmbeddedIndex(t *testing.T) {
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("embedded index.html is empty")
	}
}

func TestHandlerNotNil(t *testing.T) {
	if Handler() == nil {
		t.Fatal("Handler() returned nil")
	}
}

// TestHasBuiltUI: the placeholder embed (index.html only) must report no built
// UI, while an embed carrying the bundler's assets directory must report one —
// that is how callers (e.g. `tmula demo`) warn that the live console is
// missing from the binary.
func TestHasBuiltUI(t *testing.T) {
	placeholder := fstest.MapFS{
		"static/index.html": &fstest.MapFile{Data: []byte("<!doctype html>")},
	}
	if hasBuiltUI(placeholder) {
		t.Error("placeholder-only embed must report no built UI")
	}
	built := fstest.MapFS{
		"static/index.html":      &fstest.MapFile{Data: []byte("<!doctype html>")},
		"static/assets/index.js": &fstest.MapFile{Data: []byte("app")},
	}
	if !hasBuiltUI(built) {
		t.Error("an embed with bundled assets must report a built UI")
	}
	empty := fstest.MapFS{
		"static/index.html": &fstest.MapFile{Data: []byte("<!doctype html>")},
		"static/assets":     &fstest.MapFile{Mode: fs.ModeDir | 0o755},
	}
	if hasBuiltUI(empty) {
		t.Error("an empty assets directory must still report no built UI")
	}
}
