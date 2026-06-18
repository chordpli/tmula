package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// --- test PEM helpers (shared by jws_test.go and mint_test.go) ---------------

// cryptoSHA256 exposes the crypto.Hash the RS256 verify test passes to
// rsa.VerifyPKCS1v15, kept here so the jws test reads cleanly.
func cryptoSHA256() crypto.Hash { return crypto.SHA256 }

func marshalPKCS8(t *testing.T, priv *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8 rsa: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func marshalECPKCS8(t *testing.T, priv *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8 ec: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// decodeClaims reads the payload segment of a compact JWS into a generic claims
// map for assertions.
func decodeClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q is not a 3-part JWS", token)
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(body, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return claims
}

// --- MintProvider ------------------------------------------------------------

// TestMintProviderAcquirePerIndex mints a token per virtual user, asserting the
// per-VU sub differs (userIndex 0 vs 1), exp is set from the TTL, and the alg is
// in the header. It is the M1 path: no login/refresh/capture, a token per index.
func TestMintProviderAcquirePerIndex(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	spec := domain.MintSpec{
		Alg:            domain.MintHS256,
		SecretEncoding: domain.MintEncodingRaw,
		Subject:        "user-{{.userIndex}}",
		Claims:         map[string]string{"role": "tester", "tenant": "acme"},
		TTL:            time.Hour,
	}
	p, err := NewMintProvider(spec, []byte("symmetric-secret"), func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewMintProvider: %v", err)
	}

	c0, err := p.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatalf("Acquire(0): %v", err)
	}
	c1, err := p.Acquire(context.Background(), 1)
	if err != nil {
		t.Fatalf("Acquire(1): %v", err)
	}
	if c0.Subject == c1.Subject {
		t.Fatalf("subjects must differ per index: both %q", c0.Subject)
	}
	if c0.Subject != "user-0" || c1.Subject != "user-1" {
		t.Errorf("subjects = %q, %q; want user-0, user-1", c0.Subject, c1.Subject)
	}

	claims := decodeClaims(t, c0.Secret)
	if claims["sub"] != "user-0" {
		t.Errorf("sub claim = %v, want user-0", claims["sub"])
	}
	if claims["role"] != "tester" {
		t.Errorf("role claim = %v, want tester", claims["role"])
	}
	exp, ok := claims["exp"].(float64)
	if !ok {
		t.Fatalf("exp claim missing or not numeric: %v", claims["exp"])
	}
	if int64(exp) != now.Add(time.Hour).Unix() {
		t.Errorf("exp = %d, want %d (now+ttl)", int64(exp), now.Add(time.Hour).Unix())
	}
	iat, ok := claims["iat"].(float64)
	if !ok || int64(iat) != now.Unix() {
		t.Errorf("iat = %v, want %d", claims["iat"], now.Unix())
	}

	// The per-VU sub differs ⇒ the signed token differs ⇒ the secrets differ.
	if c0.Secret == c1.Secret {
		t.Errorf("minted tokens must differ per index")
	}
}

// TestMintProviderDeterministic mints the SAME token for the same index when the
// clock is fixed — deterministic per index, as the spec requires.
func TestMintProviderDeterministic(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	spec := domain.MintSpec{Alg: domain.MintHS256, SecretEncoding: domain.MintEncodingRaw, Subject: "u{{.userIndex}}", TTL: time.Minute}
	p, err := NewMintProvider(spec, []byte("k"), func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewMintProvider: %v", err)
	}
	a, err := p.Acquire(context.Background(), 3)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	b, err := p.Acquire(context.Background(), 3)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if a.Secret != b.Secret {
		t.Errorf("Acquire(3) is not deterministic: %q != %q", a.Secret, b.Secret)
	}
}

// TestMintProviderHSEncodings mints a verifiable token whether the HS secret is
// declared raw, base64, or base64url. The provider decodes the on-disk/env body
// per the declared encoding into the same raw key bytes, so all three verify.
func TestMintProviderHSEncodings(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	raw := []byte("the-shared-hmac-secret-bytes")
	cases := []struct {
		name string
		enc  domain.MintEncoding
		body []byte
	}{
		{"raw", domain.MintEncodingRaw, raw},
		{"base64", domain.MintEncodingBase64, []byte(base64.StdEncoding.EncodeToString(raw))},
		{"base64url", domain.MintEncodingBase64URL, []byte(base64.RawURLEncoding.EncodeToString(raw))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := domain.MintSpec{Alg: domain.MintHS256, SecretEncoding: tc.enc}.DecodeKey(tc.body)
			if err != nil {
				t.Fatalf("DecodeKey: %v", err)
			}
			spec := domain.MintSpec{Alg: domain.MintHS256, SecretEncoding: tc.enc, Subject: "u{{.userIndex}}", TTL: time.Hour}
			p, err := NewMintProvider(spec, key, func() time.Time { return now })
			if err != nil {
				t.Fatalf("NewMintProvider: %v", err)
			}
			cred, err := p.Acquire(context.Background(), 0)
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			// Verify the HS256 token with the RAW key — every encoding must reduce to it.
			parts := strings.Split(cred.Secret, ".")
			mac := hmac.New(sha256.New, raw)
			mac.Write([]byte(parts[0] + "." + parts[1]))
			want := mac.Sum(nil)
			sig, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil {
				t.Fatalf("decode sig: %v", err)
			}
			if !hmac.Equal(want, sig) {
				t.Errorf("%s-encoded HS256 token does not verify with the raw key", tc.name)
			}
		})
	}
}

// TestNewProviderBuildsMint wires CredMint through NewProvider: a mint pool builds
// a MintProvider whose Acquire returns a signed token. The resolved key rides on
// ProviderDeps.MintKey (the in-process raw bytes), never the wire spec.
func TestNewProviderBuildsMint(t *testing.T) {
	pool := domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredMint,
		Mint: &domain.MintSpec{
			Alg:            domain.MintHS256,
			SecretEncoding: domain.MintEncodingRaw,
			Subject:        "vu-{{.userIndex}}",
			TTL:            time.Hour,
		},
	}
	prov, err := NewProvider(pool, ProviderDeps{MintKey: []byte("secret")})
	if err != nil {
		t.Fatalf("NewProvider mint: %v", err)
	}
	cred, err := prov.Acquire(context.Background(), 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if cred.Subject != "vu-0" {
		t.Errorf("subject = %q, want vu-0", cred.Subject)
	}
	if strings.Count(cred.Secret, ".") != 2 {
		t.Errorf("minted secret %q is not a compact JWS", cred.Secret)
	}
	claims := decodeClaims(t, cred.Secret)
	if claims["sub"] != "vu-0" {
		t.Errorf("sub = %v, want vu-0", claims["sub"])
	}
}

// TestNewProviderMintRequiresKey refuses to build a mint provider with no resolved
// key — a mint run with an unresolved key reference is a wiring bug, not a silent
// anonymous run.
func TestNewProviderMintRequiresKey(t *testing.T) {
	pool := domain.CredentialPool{
		Strategy: domain.CredMint,
		Mint:     &domain.MintSpec{Alg: domain.MintHS256, SecretEncoding: domain.MintEncodingRaw, TTL: time.Hour},
	}
	if _, err := NewProvider(pool, ProviderDeps{}); err == nil {
		t.Fatal("a mint pool with no resolved key should be rejected")
	}
}

// TestMintProviderRS256VerifiesWithPublicKey end-to-end: the provider signs with an
// RSA PEM and the token verifies with the public key, sub per index.
func TestMintProviderRS256VerifiesWithPublicKey(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	keyPEM := marshalPKCS8(t, priv)
	now := time.Unix(1_700_000_000, 0)
	spec := domain.MintSpec{Alg: domain.MintRS256, Subject: "u{{.userIndex}}", TTL: time.Hour}
	key, err := spec.DecodeKey(keyPEM)
	if err != nil {
		t.Fatalf("DecodeKey rsa: %v", err)
	}
	p, err := NewMintProvider(spec, key, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewMintProvider: %v", err)
	}
	cred, err := p.Acquire(context.Background(), 7)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	parts := strings.Split(cred.Secret, ".")
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(&priv.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("minted RS256 token does not verify: %v", err)
	}
	if claims := decodeClaims(t, cred.Secret); claims["sub"] != "u7" {
		t.Errorf("sub = %v, want u7", claims["sub"])
	}
}
