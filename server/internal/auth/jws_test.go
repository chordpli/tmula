package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
)

// decodeSegment is the test-side base64url (no padding) decode of a compact-JWS
// segment, used to read back the header/payload a signer produced.
func decodeSegment(t *testing.T, seg string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("decode segment %q: %v", seg, err)
	}
	return b
}

// splitJWS splits a compact JWS into its three segments, failing the test if it
// is not the expected header.payload.signature shape.
func splitJWS(t *testing.T, token string) (header, payload, sig string) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q is not a 3-part compact JWS", token)
	}
	return parts[0], parts[1], parts[2]
}

// TestSignHS256RoundTrip signs a payload with HS256 and verifies the signature
// with crypto/hmac independently, asserting the header alg.
func TestSignHS256RoundTrip(t *testing.T) {
	key := []byte("a-symmetric-secret-key")
	payload := []byte(`{"sub":"u0","exp":1893456000}`)
	token, err := SignCompact(AlgHS256, key, payload)
	if err != nil {
		t.Fatalf("SignCompact HS256: %v", err)
	}
	h, p, sigSeg := splitJWS(t, token)

	var hdr struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(decodeSegment(t, h), &hdr); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if hdr.Alg != "HS256" {
		t.Errorf("header alg = %q, want HS256", hdr.Alg)
	}
	if hdr.Typ != "JWT" {
		t.Errorf("header typ = %q, want JWT", hdr.Typ)
	}
	if got := decodeSegment(t, p); string(got) != string(payload) {
		t.Errorf("payload = %q, want %q", got, payload)
	}

	// Independent verify: HMAC-SHA256 over "header.payload".
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(h + "." + p))
	want := mac.Sum(nil)
	got := decodeSegment(t, sigSeg)
	if !hmac.Equal(got, want) {
		t.Errorf("HS256 signature does not verify")
	}
}

// TestSignRS256RoundTrip signs with an RSA key and verifies with the PUBLIC key.
func TestSignRS256RoundTrip(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	pem := marshalPKCS8(t, priv)
	payload := []byte(`{"sub":"u1","exp":1893456000}`)
	token, err := SignCompact(AlgRS256, pem, payload)
	if err != nil {
		t.Fatalf("SignCompact RS256: %v", err)
	}
	h, p, sigSeg := splitJWS(t, token)

	var hdr struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(decodeSegment(t, h), &hdr); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if hdr.Alg != "RS256" {
		t.Errorf("header alg = %q, want RS256", hdr.Alg)
	}

	digest := sha256.Sum256([]byte(h + "." + p))
	if err := rsa.VerifyPKCS1v15(&priv.PublicKey, cryptoSHA256(), digest[:], decodeSegment(t, sigSeg)); err != nil {
		t.Errorf("RS256 signature does not verify with the public key: %v", err)
	}
}

// TestSignES256RoundTrip signs with a P-256 key and verifies with the public key,
// asserting the signature is the FIXED-WIDTH R||S form (64 bytes for P-256), per
// RFC 7518 — NOT the ASN.1 DER ecdsa.Sign returns.
func TestSignES256RoundTrip(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa key: %v", err)
	}
	pem := marshalECPKCS8(t, priv)
	payload := []byte(`{"sub":"u2","exp":1893456000}`)
	token, err := SignCompact(AlgES256, pem, payload)
	if err != nil {
		t.Fatalf("SignCompact ES256: %v", err)
	}
	h, p, sigSeg := splitJWS(t, token)

	var hdr struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(decodeSegment(t, h), &hdr); err != nil {
		t.Fatalf("decode header: %v", err)
	}
	if hdr.Alg != "ES256" {
		t.Errorf("header alg = %q, want ES256", hdr.Alg)
	}

	raw := decodeSegment(t, sigSeg)
	if len(raw) != 64 {
		t.Fatalf("ES256 signature length = %d, want fixed-width 64 (R||S for P-256)", len(raw))
	}
	r := new(big.Int).SetBytes(raw[:32])
	sVal := new(big.Int).SetBytes(raw[32:])
	digest := sha256.Sum256([]byte(h + "." + p))
	if !ecdsa.Verify(&priv.PublicKey, digest[:], r, sVal) {
		t.Errorf("ES256 fixed-width signature does not verify with the public key")
	}
}

// TestSignES256RejectsNonP256Curve proves the signer self-defends: DecodeKey gates
// the curve at key resolution, but SignCompact is exported, so a caller that hands a
// P-384 key to ES256 must be refused rather than emitting a wrong-width R||S.
func TestSignES256RejectsNonP256Curve(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa P-384 key: %v", err)
	}
	pem := marshalECPKCS8(t, priv)
	if _, err := SignCompact(AlgES256, pem, []byte(`{"sub":"u"}`)); err == nil {
		t.Fatal("ES256 must reject a non-P-256 (P-384) key")
	}
}

// TestSignCompactRejectsUnknownAlg refuses an alg the signer does not implement.
func TestSignCompactRejectsUnknownAlg(t *testing.T) {
	if _, err := SignCompact("HS512", []byte("k"), []byte(`{}`)); err == nil {
		t.Fatal("an unknown alg should be rejected")
	}
}
