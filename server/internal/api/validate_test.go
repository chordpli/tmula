package api

import (
	"net/http"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
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

// TestClosedRequiresPoolOrCount asserts a closed run is valid when it carries
// either an explicit user array or a positive UserCount, and rejected when it has
// neither — the count now satisfies the "at least one virtual user" rule so a large
// pool can be requested as a number instead of a shipped array.
func TestClosedRequiresPoolOrCount(t *testing.T) {
	cp, closeCP := newCP(t)
	defer closeCP()
	sut := sutOK()
	defer sut.Close()

	// Neither a user array nor a count → rejected (VirtualUserCount stays positive,
	// so this isolates the pool check, not the experiment-params check).
	spec := specFor(sut.URL, 1)
	spec.Users = nil
	spec.UserCount = 0
	resp := postJSON(t, cp.URL+"/experiments", spec)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty pool + zero count = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// A positive count alone is a valid closed pool (synthesized server-side).
	resp = postJSON(t, cp.URL+"/experiments", specForCount(sut.URL, 5))
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("count-only pool = %d, want 201", resp.StatusCode)
	}
	resp.Body.Close()
}
