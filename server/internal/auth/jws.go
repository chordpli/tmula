package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"

	"github.com/chordpli/tmula/server/internal/domain"
)

// jws.go is a thin, STANDARD-LIBRARY-ONLY JOSE/compact-JWS encoder for the mint
// strategy: it builds a compact JWS — base64url(header).base64url(payload).
// base64url(signature) — for the three algorithms tmula self-issues with. There is
// NO third-party JWT/JOSE dependency (and a test guards go.mod against one):
//
//   - HS256: HMAC-SHA256 (crypto/hmac + crypto/sha256) over the signing input.
//   - RS256: RSA PKCS#1 v1.5 over SHA-256 (crypto/rsa).
//   - ES256: ECDSA on P-256 over SHA-256 (crypto/ecdsa), with the signature encoded
//     as the FIXED-WIDTH R||S form RFC 7518 mandates (NOT the ASN.1 DER ecdsa.Sign
//     returns) — 32 bytes of R followed by 32 bytes of S, each left-zero-padded.

// Re-export the algorithm constants under the auth package so the signer's callers
// (and tests) name them without reaching into domain for the spelling.
const (
	AlgHS256 = domain.MintHS256
	AlgRS256 = domain.MintRS256
	AlgES256 = domain.MintES256
)

// p256SigBytes is the fixed width of one ES256 signature coordinate (P-256 is a
// 256-bit curve ⇒ 32 bytes); a full R||S signature is twice this (64 bytes).
const p256SigBytes = 32

// jwsHeader is the protected header of a compact JWS: the signing algorithm and a
// JWT type. tmula mints plain JWTs, so no kid/jku is set.
type jwsHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// SignCompact builds a compact JWS over payload, signing with key under alg. key is
// the resolved in-process signing material the MintProvider holds: the raw HMAC
// secret for HS256, or the PEM private-key bytes for RS256/ES256. It returns the
// "header.payload.signature" string (each segment base64url, no padding). An unknown
// alg, an unusable key, or a signing failure is a loud error.
func SignCompact(alg domain.MintAlg, key, payload []byte) (string, error) {
	hdr := jwsHeader{Alg: string(alg), Typ: "JWT"}
	hdrJSON, err := json.Marshal(hdr)
	if err != nil {
		return "", fmt.Errorf("auth: marshal jws header: %w", err)
	}
	signingInput := b64(hdrJSON) + "." + b64(payload)

	sig, err := signBytes(alg, key, []byte(signingInput))
	if err != nil {
		return "", err
	}
	return signingInput + "." + b64(sig), nil
}

// signBytes produces the raw signature bytes for the signing input under alg.
func signBytes(alg domain.MintAlg, key, signingInput []byte) ([]byte, error) {
	switch alg {
	case domain.MintHS256:
		mac := hmac.New(sha256.New, key)
		mac.Write(signingInput)
		return mac.Sum(nil), nil

	case domain.MintRS256:
		priv, err := rsaPrivateFromPEM(key)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(signingInput)
		sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
		if err != nil {
			return nil, fmt.Errorf("auth: RS256 sign: %w", err)
		}
		return sig, nil

	case domain.MintES256:
		priv, err := ecdsaPrivateFromPEM(key)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(signingInput)
		r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
		if err != nil {
			return nil, fmt.Errorf("auth: ES256 sign: %w", err)
		}
		// RFC 7518 §3.4: the JWS signature is the fixed-width concatenation R||S, each
		// coordinate left-zero-padded to the curve's byte length — NOT the ASN.1 DER
		// ecdsa.Sign returns. A verifier (and a JWKS consumer) expects exactly 64 bytes.
		return ecdsaR_S(r, s), nil

	default:
		return nil, fmt.Errorf("auth: unsupported signing alg %q (want %q, %q or %q)", alg, domain.MintHS256, domain.MintRS256, domain.MintES256)
	}
}

// ecdsaR_S encodes an ECDSA (r, s) pair as the fixed-width R||S byte string for
// P-256: each integer is serialized big-endian and left-zero-padded to 32 bytes, so
// the result is always exactly 64 bytes regardless of the integers' magnitude.
func ecdsaR_S(r, s *big.Int) []byte {
	out := make([]byte, 2*p256SigBytes)
	r.FillBytes(out[:p256SigBytes])
	s.FillBytes(out[p256SigBytes:])
	return out
}

// rsaPrivateFromPEM parses the PEM bytes into an RSA private key (PKCS#8 or PKCS#1).
func rsaPrivateFromPEM(body []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, fmt.Errorf("auth: RS256 key is not a PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("auth: parse RS256 key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("auth: RS256 key is not RSA (%T)", k)
	}
	return rk, nil
}

// ecdsaPrivateFromPEM parses the PEM bytes into an ECDSA private key (PKCS#8 or SEC1).
func ecdsaPrivateFromPEM(body []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, fmt.Errorf("auth: ES256 key is not a PEM block")
	}
	ek, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		k, perr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if perr != nil {
			return nil, fmt.Errorf("auth: parse ES256 key: %w", perr)
		}
		var ok bool
		ek, ok = k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("auth: ES256 key is not ECDSA (%T)", k)
		}
	}
	// Defense in depth: ES256 is P-256 only (RFC 7518). DecodeKey already rejects a
	// non-P-256 curve at the single key-resolution gate, but SignCompact is exported,
	// so the signer self-defends against a caller that hands it another curve (a
	// P-384 key would otherwise sign with a truncated/over-wide R||S).
	if ek.Curve != elliptic.P256() {
		return nil, fmt.Errorf("auth: ES256 requires a P-256 key, got %s", ek.Curve.Params().Name)
	}
	return ek, nil
}

// b64 is base64url with no padding — the JWS segment encoding (RFC 7515).
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
