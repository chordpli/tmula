package api

import (
	"net/http"
	"testing"

	"github.com/chordpli/tmula/internal/domain"
)

// TestRejectsUnsafeTemplatePath ensures a template path that could redirect a
// request off the target host (authority/scheme/CRLF/unrooted) is rejected at
// experiment-creation time.
func TestRejectsUnsafeTemplatePath(t *testing.T) {
	cp, closeCP := newCP(t)
	defer closeCP()
	sut := sutOK()
	defer sut.Close()

	bad := []string{
		"@evil.com/x",   // userinfo trick overrides the authority
		"//evil.com/x",  // protocol-relative authority
		"http://evil/x", // absolute URL with a scheme
		"/x\r\nHost: e", // CRLF injection
		"noslash",       // not a rooted path
	}
	for _, p := range bad {
		spec := specFor(sut.URL, 1)
		spec.Templates["ta"] = domain.APITemplate{Method: "GET", Path: p}
		resp := postJSON(t, cp.URL+"/experiments", spec)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("template path %q = %d, want 400", p, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// A normal rooted path is accepted.
	spec := specFor(sut.URL, 1)
	spec.Templates["ta"] = domain.APITemplate{Method: "GET", Path: "/a"}
	resp := postJSON(t, cp.URL+"/experiments", spec)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("normal path = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()
}
