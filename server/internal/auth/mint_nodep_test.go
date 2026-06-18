package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestMintAddsNoJWTDependency is the no-new-dependency guard for the mint strategy:
// JWT signing must use the standard library only (crypto/hmac, crypto/rsa,
// crypto/ecdsa), never a third-party JOSE/JWT module. It reads the repo go.mod and
// fails if a known JWT library appears, so a future "just import a jwt package"
// shortcut is caught here rather than in review.
func TestMintAddsNoJWTDependency(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file to find go.mod")
	}
	// server/internal/auth/mint_nodep_test.go -> repo root is four levels up.
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	body := strings.ToLower(string(gomod))
	forbidden := []string{
		"golang-jwt", "dgrijalva/jwt", "lestrrat-go/jwx", "go-jose", "square/go-jose",
		"gopkg.in/square", "jwt-go", "kataras/jwt",
	}
	for _, dep := range forbidden {
		if strings.Contains(body, dep) {
			t.Errorf("go.mod gained a JWT dependency %q; mint signing must use the standard library only", dep)
		}
	}
}
