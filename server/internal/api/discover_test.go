package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/load"
)

// postDiscover submits a discover request to POST /auth/discover and returns the HTTP
// status and decoded result.
func postDiscover(t *testing.T, srv *Server, req DiscoverRequest) (int, DiscoverResult) {
	t.Helper()
	b, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/auth/discover", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httpReq)
	var res DiscoverResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	return rec.Code, res
}

// fakeIdP serves an OpenID Connect discovery document (whatever body/status the caller
// configures) at the well-known path.
func fakeIdP(body string, status int) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	return httptest.NewServer(mux)
}

// TestDiscoverHappyPath fetches a valid discovery document and returns its token endpoint
// and supported grant types — the "paste your issuer URL → token URL auto-filled" flow.
func TestDiscoverHappyPath(t *testing.T) {
	idp := fakeIdP(`{"issuer":"https://idp","token_endpoint":"https://idp/oauth/token","grant_types_supported":["authorization_code","client_credentials"]}`, http.StatusOK)
	defer idp.Close()
	srv := NewServer(load.NewRESTAdapter(0))

	code, res := postDiscover(t, srv, DiscoverRequest{Issuer: idp.URL, Allow: []string{"127.0.0.1"}})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !res.OK {
		t.Fatalf("discover failed: %+v", res)
	}
	if res.TokenEndpoint != "https://idp/oauth/token" {
		t.Errorf("tokenEndpoint = %q", res.TokenEndpoint)
	}
	if len(res.GrantTypesSupported) != 2 {
		t.Errorf("grantTypesSupported = %v, want 2 entries", res.GrantTypesSupported)
	}
}

// TestDiscoverAcceptsFullWellKnownURL proves a caller may paste either the bare issuer or
// the full .../.well-known/openid-configuration URL.
func TestDiscoverAcceptsFullWellKnownURL(t *testing.T) {
	idp := fakeIdP(`{"token_endpoint":"https://idp/token"}`, http.StatusOK)
	defer idp.Close()
	srv := NewServer(load.NewRESTAdapter(0))

	code, res := postDiscover(t, srv, DiscoverRequest{
		Issuer: idp.URL + "/.well-known/openid-configuration",
		Allow:  []string{"127.0.0.1"},
	})
	if code != http.StatusOK || !res.OK {
		t.Fatalf("discover with full URL failed: code=%d res=%+v", code, res)
	}
	if res.TokenEndpoint != "https://idp/token" {
		t.Errorf("tokenEndpoint = %q", res.TokenEndpoint)
	}
}

// TestDiscoverAllowlistReject proves the SSRF gate: an issuer host outside the allowlist is
// refused with 403 and an actionable "add <host> to the allowlist" message, before any
// fetch is attempted.
func TestDiscoverAllowlistReject(t *testing.T) {
	idp := fakeIdP(`{"token_endpoint":"https://idp/token"}`, http.StatusOK)
	defer idp.Close()
	srv := NewServer(load.NewRESTAdapter(0))

	// The allowlist does NOT include 127.0.0.1 (the fake IdP's host).
	code, _ := postDiscover(t, srv, DiscoverRequest{Issuer: idp.URL, Allow: []string{"idp.example.com"}})
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (issuer host not allowlisted)", code)
	}
	// The error body names the host to add.
	b, _ := json.Marshal(DiscoverRequest{Issuer: idp.URL, Allow: []string{"idp.example.com"}})
	req := httptest.NewRequest(http.MethodPost, "/auth/discover", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "allowlist") {
		t.Errorf("403 body should tell the operator to add the host to the allowlist, got %s", rec.Body.String())
	}
}

// TestDiscoverMalformedDoc reports ok:false when the fetched document is JSON without a
// token_endpoint.
func TestDiscoverMalformedDoc(t *testing.T) {
	idp := fakeIdP(`{"issuer":"https://idp","jwks_uri":"https://idp/jwks"}`, http.StatusOK)
	defer idp.Close()
	srv := NewServer(load.NewRESTAdapter(0))

	code, res := postDiscover(t, srv, DiscoverRequest{Issuer: idp.URL, Allow: []string{"127.0.0.1"}})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an ok:false body, not an HTTP error)", code)
	}
	if res.OK {
		t.Fatal("a document with no token_endpoint must be ok:false")
	}
	if !strings.Contains(res.Reason, "token_endpoint") {
		t.Errorf("reason should call out the missing token_endpoint, got %q", res.Reason)
	}
}

// TestDiscoverNon200 reports ok:false naming the URL tried when the IdP does not answer 200.
func TestDiscoverNon200(t *testing.T) {
	idp := fakeIdP(`not found`, http.StatusNotFound)
	defer idp.Close()
	srv := NewServer(load.NewRESTAdapter(0))

	code, res := postDiscover(t, srv, DiscoverRequest{Issuer: idp.URL, Allow: []string{"127.0.0.1"}})
	if code != http.StatusOK || res.OK {
		t.Fatalf("a 404 discovery fetch should be ok:false, got code=%d res=%+v", code, res)
	}
	if !strings.Contains(res.Reason, "404") || !strings.Contains(res.Reason, "openid-configuration") {
		t.Errorf("reason should name the status and URL tried, got %q", res.Reason)
	}
}

// TestDiscoverOversizedResponse reports ok:false when the discovery document exceeds the
// 1 MiB cap, rather than buffering it.
func TestDiscoverOversizedResponse(t *testing.T) {
	// A valid-looking doc padded past the cap with a huge ignored field.
	big := `{"token_endpoint":"https://idp/token","pad":"` + strings.Repeat("x", (1<<20)+16) + `"}`
	idp := fakeIdP(big, http.StatusOK)
	defer idp.Close()
	srv := NewServer(load.NewRESTAdapter(0))

	code, res := postDiscover(t, srv, DiscoverRequest{Issuer: idp.URL, Allow: []string{"127.0.0.1"}})
	if code != http.StatusOK || res.OK {
		t.Fatalf("an oversized discovery doc should be ok:false, got code=%d res=%+v", code, res)
	}
	if !strings.Contains(res.Reason, "cap") {
		t.Errorf("reason should mention the size cap, got %q", res.Reason)
	}
}

// TestDiscoverRequiresIssuerAndAllow rejects a missing issuer (400) and a missing allowlist
// (400) — the latter because with no allowlist no host could be authorized.
func TestDiscoverRequiresIssuerAndAllow(t *testing.T) {
	srv := NewServer(load.NewRESTAdapter(0))

	if code, _ := postDiscover(t, srv, DiscoverRequest{Allow: []string{"127.0.0.1"}}); code != http.StatusBadRequest {
		t.Errorf("missing issuer: status = %d, want 400", code)
	}
	if code, _ := postDiscover(t, srv, DiscoverRequest{Issuer: "https://idp.example.com"}); code != http.StatusBadRequest {
		t.Errorf("missing allowlist: status = %d, want 400", code)
	}
	if code, _ := postDiscover(t, srv, DiscoverRequest{Issuer: "not a url ://", Allow: []string{"x"}}); code != http.StatusBadRequest {
		t.Errorf("malformed issuer: status = %d, want 400", code)
	}
}

// TestDiscoveryURLForNormalization pins the issuer→discovery-URL rule: a bare issuer gets
// the well-known suffix appended (trailing slash trimmed); a full well-known URL is kept.
func TestDiscoveryURLForNormalization(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://idp.example.com", "https://idp.example.com/.well-known/openid-configuration"},
		{"https://idp.example.com/", "https://idp.example.com/.well-known/openid-configuration"},
		{"https://idp.example.com/.well-known/openid-configuration", "https://idp.example.com/.well-known/openid-configuration"},
	}
	for _, tc := range cases {
		got, err := discoveryURLFor(tc.in)
		if err != nil {
			t.Errorf("discoveryURLFor(%q) errored: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("discoveryURLFor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := discoveryURLFor("idp.example.com"); err == nil {
		t.Error("a bare hostname with no scheme should be rejected")
	}
}
