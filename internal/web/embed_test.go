package web

import "testing"

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
