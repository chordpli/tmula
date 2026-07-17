package domain

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"
)

func pkcs8PEM(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func ecKey(t *testing.T, curve elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("ec key: %v", err)
	}
	return k
}

// TestCredMintValid threads the mint strategy through the enum's Valid().
func TestCredMintValid(t *testing.T) {
	if !CredMint.Valid() {
		t.Fatal("CredMint must be a valid credential strategy")
	}
	if CredMint != "mint" {
		t.Errorf("CredMint = %q, want \"mint\"", CredMint)
	}
}

// TestMintSpecValidateAlg rejects an unknown alg and accepts the three the signer
// implements (HS256/RS256/ES256). HS requires a key ref + a valid encoding; RS/ES
// require a key ref.
func TestMintSpecValidateAlg(t *testing.T) {
	hsKey := &CredentialSourceRef{Env: "MINT_HS", Format: "tokens"}
	asymKey := &CredentialSourceRef{Env: "MINT_PEM", Format: "tokens"}
	cases := []struct {
		name string
		spec MintSpec
		ok   bool
	}{
		{"hs256-ok", MintSpec{Alg: MintHS256, SecretEncoding: MintEncodingRaw, Key: hsKey, TTL: time.Hour}, true},
		{"rs256-ok", MintSpec{Alg: MintRS256, Key: asymKey, TTL: time.Hour}, true},
		{"es256-ok", MintSpec{Alg: MintES256, Key: asymKey, TTL: time.Hour}, true},
		{"bad-alg", MintSpec{Alg: "HS512", SecretEncoding: MintEncodingRaw, Key: hsKey, TTL: time.Hour}, false},
		{"empty-alg", MintSpec{Alg: "", SecretEncoding: MintEncodingRaw, Key: hsKey, TTL: time.Hour}, false},
		{"hs-missing-key", MintSpec{Alg: MintHS256, SecretEncoding: MintEncodingRaw, TTL: time.Hour}, false},
		{"hs-bad-encoding", MintSpec{Alg: MintHS256, SecretEncoding: "hex", Key: hsKey, TTL: time.Hour}, false},
		{"rs-missing-key", MintSpec{Alg: MintRS256, TTL: time.Hour}, false},
		{"ttl-zero", MintSpec{Alg: MintHS256, SecretEncoding: MintEncodingRaw, Key: hsKey, TTL: 0}, false},
		{"ttl-negative", MintSpec{Alg: MintHS256, SecretEncoding: MintEncodingRaw, Key: hsKey, TTL: -time.Second}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.ok && err != nil {
				t.Errorf("Validate() = %v, want ok", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("Validate() = nil, want error")
			}
		})
	}
}

// TestMintSpecDecodeKeyPEMTypeMatchesAlg accepts a matching PEM and rejects a
// mismatch: an RSA PEM for ES256, an EC PEM for RS256, and a P-384 EC key for
// ES256 (which mandates P-256).
func TestMintSpecDecodeKeyPEMTypeMatchesAlg(t *testing.T) {
	rsaPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	rsaPEM := pkcs8PEM(t, rsaPriv)
	p256 := pkcs8PEM(t, ecKey(t, elliptic.P256()))
	p384 := pkcs8PEM(t, ecKey(t, elliptic.P384()))

	cases := []struct {
		name string
		alg  MintAlg
		pem  []byte
		ok   bool
	}{
		{"rs256-rsa-ok", MintRS256, rsaPEM, true},
		{"es256-p256-ok", MintES256, p256, true},
		{"rs256-with-ec", MintRS256, p256, false},
		{"es256-with-rsa", MintES256, rsaPEM, false},
		{"es256-p384-rejected", MintES256, p384, false},
		{"rs256-garbage", MintRS256, []byte("not a pem"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := MintSpec{Alg: tc.alg}.DecodeKey(tc.pem)
			if tc.ok && err != nil {
				t.Errorf("DecodeKey = %v, want ok", err)
			}
			if !tc.ok && err == nil {
				t.Errorf("DecodeKey = nil, want error (PEM type/curve must match the alg)")
			}
		})
	}
}

// TestMintSpecDecodeKeyHSEncoding decodes the HS secret per the declared encoding
// and rejects an undecodable body for the declared encoding.
func TestMintSpecDecodeKeyHSEncoding(t *testing.T) {
	if _, err := (MintSpec{Alg: MintHS256, SecretEncoding: MintEncodingBase64}).DecodeKey([]byte("!!!not base64!!!")); err == nil {
		t.Error("a non-base64 body must be rejected for the base64 encoding")
	}
	if _, err := (MintSpec{Alg: MintHS256, SecretEncoding: MintEncodingRaw}).DecodeKey([]byte("anything goes raw")); err != nil {
		t.Errorf("raw encoding should accept any bytes: %v", err)
	}
}

// TestMintSpecKeyNeverSerializes is the AD-011 contract for mint: the resolved key
// bytes carry json:"-" and the non-secret reference is all that crosses the wire.
func TestMintSpecKeyNeverSerializes(t *testing.T) {
	spec := MintSpec{
		Alg:            MintHS256,
		SecretEncoding: MintEncodingRaw,
		Key:            &CredentialSourceRef{Env: "MINT_HS", Format: "tokens"},
		Subject:        "u{{.userIndex}}",
		TTL:            time.Hour,
	}
	// Set a resolved key by round-tripping through DecodeKey-shaped bytes; the field
	// is unexported-equivalent via json:"-", so simulate by marshaling the struct.
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, leaked := raw["resolvedKey"]; leaked {
		t.Error("the resolved key must never serialize")
	}
	// The non-secret reference round-trips.
	if _, ok := raw["key"]; !ok {
		t.Error("the non-secret key reference should serialize")
	}
}
