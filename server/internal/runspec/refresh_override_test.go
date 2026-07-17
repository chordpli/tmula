package runspec_test

import (
	"strings"
	"testing"
)

// TestValidateRefreshOverrideShape pins the LoginFlowSpec refresh-override shape
// check: an EMPTY RefreshRequest is valid (auto-derive / default-endpoint path); a
// SET RefreshRequest must be a well-formed "METHOD /path", else it is rejected. The
// override carries no secret (the refresh token is captured at run time), so no
// secret-shape check is needed — only the request line is shape-validated.
func TestValidateRefreshOverrideShape(t *testing.T) {
	// Empty override → valid (auto-derive).
	empty := loginSpec("http://127.0.0.1:1")
	empty.LoginFlow.RefreshRequest = ""
	empty.LoginFlow.RefreshBody = ""
	if err := empty.Validate(); err != nil {
		t.Errorf("an empty refresh override must be valid (auto-derive): %v", err)
	}

	// Body-only override (no request line) → valid: the method/path default to the
	// login token endpoint at compile time.
	bodyOnly := loginSpec("http://127.0.0.1:1")
	bodyOnly.LoginFlow.RefreshRequest = ""
	bodyOnly.LoginFlow.RefreshBody = "grant_type=refresh_token&refresh_token={{.refreshToken}}"
	if err := bodyOnly.Validate(); err != nil {
		t.Errorf("a body-only refresh override must be valid: %v", err)
	}

	// A well-formed request line → valid.
	good := loginSpec("http://127.0.0.1:1")
	good.LoginFlow.RefreshRequest = "POST /oauth/token"
	good.LoginFlow.RefreshBody = "grant_type=refresh_token&refresh_token={{.refreshToken}}"
	if err := good.Validate(); err != nil {
		t.Errorf("a well-formed refresh request line must be valid: %v", err)
	}

	// Malformed request lines → rejected, only when set.
	for _, bad := range []string{
		"GET",                // missing path
		"POST /a /b",         // extra field
		"POST relative/path", // path not rooted
		"POST",               // no path
		"  ",                 // whitespace only is NOT empty after trim? (treated as empty → valid; see below)
	} {
		if strings.TrimSpace(bad) == "" {
			continue // whitespace-only is treated as empty (auto-derive), not malformed
		}
		s := loginSpec("http://127.0.0.1:1")
		s.LoginFlow.RefreshRequest = bad
		if err := s.Validate(); err == nil {
			t.Errorf("a malformed refresh request %q must be rejected", bad)
		}
	}
}
