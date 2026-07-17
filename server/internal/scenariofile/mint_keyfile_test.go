package scenariofile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mintFileYAML authors an HS256 mint scenario whose signing key is a FILE reference
// resolved against the scenario file's directory (a bare filename, no directory).
const mintFileYAML = `
target: http://localhost:9000
flow:
  - id: a
    request: GET /a
    headers:
      Authorization: "Bearer {{.token}}"
auth:
  strategy: mint
  mint:
    alg: HS256
    secretEncoding: raw
    key:
      file: signing.key
    subject: "user-{{.userIndex}}"
    ttl: 1h
`

// TestMintKeyFileRootedAtScenarioDir is the regression for the key.file resolution bug:
// a mint scenario in a temp dir with its signing key beside it must resolve that key
// against the SCENARIO directory, not the process CWD. The test's CWD is the package
// directory (which holds no signing.key), so a successful sign proves the key was found
// relative to the scenario file, exactly as the doc comment promises.
func TestMintKeyFileRootedAtScenarioDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "signing.key"), []byte("super-secret-hmac-key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	s, err := Parse([]byte(mintFileYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// ExpandFrom records the scenario directory on the mint spec (keyRoot); the key is
	// still resolved lazily, so Expand itself never needs the file present.
	spec, err := ExpandFrom(s, dir)
	if err != nil {
		t.Fatalf("ExpandFrom: %v", err)
	}
	if got := spec.CredentialPool.Mint.KeyRoot(); got != dir {
		t.Fatalf("keyRoot = %q, want the scenario dir %q", got, dir)
	}

	// Build the provider and mint one token: this exercises run-time key resolution,
	// which must find signing.key beside the scenario (dir), not in the CWD.
	provider, err := spec.CredentialProvider()
	if err != nil {
		t.Fatalf("CredentialProvider (key should resolve against the scenario dir): %v", err)
	}
	cred, err := provider.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if cred.Secret == "" {
		t.Error("expected a signed token from the mint provider")
	}
	if cred.Subject != "user-0" {
		t.Errorf("subject = %q, want user-0", cred.Subject)
	}
}

// TestMintKeyFileNotFoundNamesDirectory proves a missing key file fails with an error
// that NAMES the directory it searched, so an operator can see it looked in the wrong
// place (the classic "resolved against CWD instead of the scenario" symptom).
func TestMintKeyFileNotFoundNamesDirectory(t *testing.T) {
	dir := t.TempDir() // deliberately empty — no signing.key here

	s, err := Parse([]byte(mintFileYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	spec, err := ExpandFrom(s, dir)
	if err != nil {
		t.Fatalf("ExpandFrom: %v", err)
	}
	_, err = spec.CredentialProvider()
	if err == nil {
		t.Fatal("a missing key file should fail provider construction")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("not-found error should name the directory it searched (%q), got %q", dir, err.Error())
	}
	if !strings.Contains(err.Error(), "signing.key") {
		t.Errorf("not-found error should name the key file, got %q", err.Error())
	}
}
