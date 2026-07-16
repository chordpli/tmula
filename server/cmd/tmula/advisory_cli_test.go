package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/api"
	"github.com/chordpli/tmula/server/internal/domain"
)

// oidcOpenAPI is an OpenAPI doc whose security scheme is OpenID Connect, so the importer
// derives both the openidconnect-discovery and mint-managed-idp advisories.
const oidcOpenAPI = "openapi: 3.0.0\n" +
	"servers:\n  - url: http://svc.test\n" +
	"components:\n  securitySchemes:\n    oidc:\n      type: openIdConnect\n" +
	"      openIdConnectUrl: https://login.example.com/.well-known/openid-configuration\n" +
	"security:\n  - oidc: []\n" +
	"paths:\n  /items:\n    get:\n      operationId: listItems\n"

// TestInitPrintsAuthAdvisories proves `tmula init` surfaces the importer's auth
// advisories as human-readable "note:" lines on stderr, so an operator sees the
// managed-IdP mint footgun and the discovery pointer before choosing a strategy.
func TestInitPrintsAuthAdvisories(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "api.yaml")
	if err := os.WriteFile(in, []byte(oidcOpenAPI), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := filepath.Join(dir, "scenario.yaml")

	stderr, err := captureStderr(t, func() error {
		return initScenario([]string{"--from", in, "--out", out})
	})
	if err != nil {
		t.Fatalf("initScenario: %v", err)
	}
	if !strings.Contains(stderr, "note:") {
		t.Fatalf("init should print advisory note lines, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "managed IdP") {
		t.Errorf("init should surface the managed-IdP mint advisory, got:\n%s", stderr)
	}
	if !strings.Contains(stderr, "OpenID Connect") {
		t.Errorf("init should surface the OpenID Connect discovery advisory, got:\n%s", stderr)
	}
}

// TestWarnMintManagedIdP proves `tmula run` warns when the mint strategy coexists with a
// mint-managed-idp advisory, and stays silent otherwise (non-mint strategy, or no
// advisory).
func TestWarnMintManagedIdP(t *testing.T) {
	mintSpec := func(advisories []domain.AuthAdvisory) api.RunSpec {
		return api.RunSpec{
			CredentialPool: &domain.CredentialPool{ID: "p", Strategy: domain.CredMint},
			AuthAdvisories: advisories,
		}
	}

	nilErr := func(fn func()) func() error { return func() error { fn(); return nil } }

	// Mint + managed-IdP advisory → warning.
	warn, _ := captureStderr(t, nilErr(func() {
		warnMintManagedIdP(mintSpec([]domain.AuthAdvisory{{Code: domain.AdvisoryMintManagedIDP, Detail: "login.okta.com"}}))
	}))
	if !strings.Contains(warn, "warning:") || !strings.Contains(warn, "login.okta.com") {
		t.Errorf("expected a managed-IdP warning naming the host, got %q", warn)
	}

	// Mint but no advisory → silent.
	if got, _ := captureStderr(t, nilErr(func() { warnMintManagedIdP(mintSpec(nil)) })); got != "" {
		t.Errorf("a mint run with no advisory should not warn, got %q", got)
	}

	// A non-mint strategy carrying the advisory → silent (the footgun is mint-only).
	poolWithAdvisory := api.RunSpec{
		CredentialPool: &domain.CredentialPool{ID: "p", Strategy: domain.CredPool},
		AuthAdvisories: []domain.AuthAdvisory{{Code: domain.AdvisoryMintManagedIDP, Detail: "login.okta.com"}},
	}
	if got, _ := captureStderr(t, nilErr(func() { warnMintManagedIdP(poolWithAdvisory) })); got != "" {
		t.Errorf("a non-mint run should not warn about the mint footgun, got %q", got)
	}
}
