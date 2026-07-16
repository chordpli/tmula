package domain

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"time"
)

// MintAlg names the JWS signing algorithm the mint strategy self-issues a token
// with. Only the three the standard-library signer implements are accepted: HS256
// (HMAC-SHA256, a symmetric secret), RS256 (RSA PKCS#1 v1.5 + SHA-256) and ES256
// (ECDSA P-256 + SHA-256). Each is a JWA "alg" header value (RFC 7518).
type MintAlg string

const (
	MintHS256 MintAlg = "HS256"
	MintRS256 MintAlg = "RS256"
	MintES256 MintAlg = "ES256"
)

// Valid reports whether a is a signing algorithm the mint signer implements.
func (a MintAlg) Valid() bool {
	switch a {
	case MintHS256, MintRS256, MintES256:
		return true
	default:
		return false
	}
}

// IsHMAC reports whether the algorithm signs with a symmetric secret (HS*) rather
// than an asymmetric private key (RS*/ES*). The two key shapes are resolved and
// validated differently: HS reads a declared-encoding secret, RS/ES read a PEM.
func (a MintAlg) IsHMAC() bool { return a == MintHS256 }

// MintEncoding declares how an HS256 secret BODY (read from the env var or file the
// key reference points at) is encoded, so the same raw key bytes are recovered
// wherever the secret is supplied. It is meaningful only for HS256; an asymmetric
// alg reads a PEM and ignores it.
type MintEncoding string

const (
	// MintEncodingRaw uses the body bytes verbatim as the HMAC key.
	MintEncodingRaw MintEncoding = "raw"
	// MintEncodingBase64 decodes the body as standard base64.
	MintEncodingBase64 MintEncoding = "base64"
	// MintEncodingBase64URL decodes the body as URL-safe base64 (no padding).
	MintEncodingBase64URL MintEncoding = "base64url"
)

// Valid reports whether e is a known HS secret encoding.
func (e MintEncoding) Valid() bool {
	switch e {
	case MintEncodingRaw, MintEncodingBase64, MintEncodingBase64URL:
		return true
	default:
		return false
	}
}

// MintSpec configures the CredMint strategy: how to self-issue a JWT per virtual
// user by signing one LOCALLY with a key the operator holds. tmula forges valid
// tokens with it, so the signing key is a REFERENCE only (Key) — an env var or a
// file the operator controls, resolved in-process at provider-build time and held
// on resolvedKey (json:"-", like Credential.Secret) — and NEVER inlined on the
// wire or serialized.
//
// The claims template is signed into each token: values may reference
// {{.userIndex}} (so sub differs per VU) and {{.subject}} (the rendered Subject),
// and Subject is the source for the per-VU sub claim recorded as the credential's
// (non-sensitive) subject for evidence. TTL becomes exp (now+ttl); the optional
// Leeway shifts a default-off nbf (now-leeway) for clock skew.
type MintSpec struct {
	// Alg is the JWS signing algorithm (HS256 | RS256 | ES256).
	Alg MintAlg `json:"alg"`
	// SecretEncoding declares how the HS256 secret body is encoded (raw | base64 |
	// base64url). Required for HS256, ignored for the asymmetric algs.
	SecretEncoding MintEncoding `json:"secretEncoding,omitempty"`
	// Key is the NON-SECRET reference to the signing key — an env var or a file
	// (reusing the same file/env reference shape pool sources use). For HS256 it
	// points at the symmetric secret; for RS256/ES256 at a PEM private key. The
	// resolved bytes are held on resolvedKey, never serialized.
	Key *CredentialSourceRef `json:"key,omitempty"`
	// Subject is the template for the per-VU `sub` claim, e.g. "user-{{.userIndex}}",
	// so each virtual user signs a distinct principal. It is also recorded as the
	// credential's subject. Optional: an empty Subject mints no sub claim.
	Subject string `json:"subject,omitempty"`
	// Claims is a template map signed into every token alongside the standard claims.
	// Values may reference {{.userIndex}} and {{.subject}}. Optional.
	Claims map[string]string `json:"claims,omitempty"`
	// TTL is the access token lifetime; it becomes exp = now+ttl. Required (> 0).
	TTL time.Duration `json:"ttl"`
	// Leeway, when > 0, sets nbf = now-leeway (and is otherwise off), absorbing
	// small clock skew between tmula and the verifier. Optional, default off.
	Leeway time.Duration `json:"leeway,omitempty"`

	// resolvedKey holds the in-process raw signing-key bytes the provider signs with:
	// the decoded HMAC secret for HS256, or the PEM bytes for RS256/ES256. It carries
	// json:"-" so a persisted or streamed spec NEVER leaks it (AD-011), exactly like
	// Credential.Secret. It is set by the layer that resolves the Key reference (the
	// same layer pool entries are resolved), never authored on the wire.
	resolvedKey []byte `json:"-"`

	// keyRoot is the directory a relative Key.File is resolved against at run time — the
	// scenario file's directory the CLI recorded at expand time, so key.file resolves
	// beside the scenario rather than against the process CWD (the documented contract).
	// It is a NON-SECRET path but carries json:"-" so it never crosses the wire: a
	// distributed worker resolves the key against its OWN root, not the master's. An empty
	// keyRoot falls back to the process working directory. Irrelevant to an env Key.
	keyRoot string `json:"-"`
}

// Validate checks the mint spec is signable: a known alg, a positive TTL, a present
// key reference, and — for HS256 — a known secret encoding. It validates SHAPE only
// (the reference, the alg, the lifetime); it does NOT read the key or check the PEM
// type, which happens at resolution time (DecodeKey), a layer above the domain.
func (m MintSpec) Validate() error {
	if !m.Alg.Valid() {
		return fmt.Errorf("mint: invalid alg %q (want %q, %q or %q)", m.Alg, MintHS256, MintRS256, MintES256)
	}
	if m.TTL <= 0 {
		return fmt.Errorf("mint: ttl must be > 0 (it sets the token's exp)")
	}
	if m.Key == nil {
		return fmt.Errorf("mint: a key reference (env or file) is required — the signing key is never inlined")
	}
	if err := m.Key.Validate(); err != nil {
		// The key reference reuses CredentialSourceRef's shape check (exactly one of
		// file/env), but its Format is irrelevant to a key body, so only the file/env
		// exclusivity matters here; tolerate an empty/foreign Format.
		if !isSourceRefShapeOK(*m.Key) {
			return fmt.Errorf("mint: key reference: %w", err)
		}
	}
	if m.Alg.IsHMAC() {
		if !m.SecretEncoding.Valid() {
			return fmt.Errorf("mint: HS256 needs a secretEncoding (%q, %q or %q)", MintEncodingRaw, MintEncodingBase64, MintEncodingBase64URL)
		}
	}
	return nil
}

// ValidateResolved is the provider-build-time check, AFTER the key reference has been
// resolved into raw bytes (DecodeKey + WithResolvedKey): the alg must be signable and
// the TTL positive. It deliberately does NOT require the Key reference (the reference
// has done its job once the bytes are resolved) so a provider can be built from a spec
// carrying only resolved bytes — the seam tests and the run path build through.
func (m MintSpec) ValidateResolved() error {
	if !m.Alg.Valid() {
		return fmt.Errorf("mint: invalid alg %q (want %q, %q or %q)", m.Alg, MintHS256, MintRS256, MintES256)
	}
	if m.TTL <= 0 {
		return fmt.Errorf("mint: ttl must be > 0 (it sets the token's exp)")
	}
	return nil
}

// isSourceRefShapeOK reports whether a key reference sets exactly one of File/Env
// (the only constraint a signing-key reference needs — its Format is irrelevant to a
// key body, unlike a credential pool source).
func isSourceRefShapeOK(r CredentialSourceRef) bool {
	hasFile, hasEnv := r.File != "", r.Env != ""
	return hasFile != hasEnv
}

// WithResolvedKey returns a copy of the spec carrying the in-process raw signing-key
// bytes (decoded via DecodeKey). It is how the resolution layer attaches the secret
// without it ever touching the wire: the returned value signs with key, and the
// secret stays json:"-". The receiver is unchanged.
func (m MintSpec) WithResolvedKey(key []byte) MintSpec {
	m.resolvedKey = key
	return m
}

// ResolvedKey returns the in-process raw signing-key bytes, or nil when none has been
// resolved yet. The signer reads it; it is never serialized.
func (m MintSpec) ResolvedKey() []byte { return m.resolvedKey }

// WithKeyRoot returns a copy of the spec carrying root as the directory a relative
// Key.File is resolved against at run time (the scenario file's directory). It is how
// the CLI records the scenario root so key.file resolves beside the scenario, not the
// process CWD; the value is non-secret and stays json:"-", so it never crosses the wire.
// The receiver is unchanged.
func (m MintSpec) WithKeyRoot(root string) MintSpec {
	m.keyRoot = root
	return m
}

// KeyRoot returns the directory a relative Key.File resolves against, or "" when none was
// recorded (the resolver then falls back to the process working directory).
func (m MintSpec) KeyRoot() string { return m.keyRoot }

// DecodeKey turns a raw key BODY (the bytes read from the env var or file the key
// reference points at) into the signing-key bytes the signer uses, validating that
// the material matches the alg:
//
//   - HS256 decodes the body per SecretEncoding (raw verbatim, or base64/base64url
//     decoded) into the HMAC secret.
//   - RS256 requires a PEM that parses to an *rsa.PrivateKey.
//   - ES256 requires a PEM that parses to an *ecdsa.PrivateKey on the P-256 curve —
//     a P-384/P-521 key is REJECTED (ES256 mandates P-256, RFC 7518).
//
// It returns the bytes the provider holds on resolvedKey: the decoded HMAC secret for
// HS256, or the (validated) PEM bytes for RS256/ES256. A type/curve mismatch, an
// unparsable PEM, or an undecodable HS body is a loud error, so a mint run never
// silently signs with the wrong key.
func (m MintSpec) DecodeKey(body []byte) ([]byte, error) {
	if m.Alg.IsHMAC() {
		switch m.SecretEncoding {
		case MintEncodingRaw:
			return body, nil
		case MintEncodingBase64:
			dec, err := base64.StdEncoding.DecodeString(string(body))
			if err != nil {
				return nil, fmt.Errorf("mint: HS256 secret is not valid base64: %w", err)
			}
			return dec, nil
		case MintEncodingBase64URL:
			dec, err := base64.RawURLEncoding.DecodeString(string(body))
			if err != nil {
				return nil, fmt.Errorf("mint: HS256 secret is not valid base64url: %w", err)
			}
			return dec, nil
		default:
			return nil, fmt.Errorf("mint: HS256 needs a secretEncoding (%q, %q or %q)", MintEncodingRaw, MintEncodingBase64, MintEncodingBase64URL)
		}
	}

	// Asymmetric: the body is a PEM private key whose type/curve must match the alg.
	priv, err := parsePrivateKeyPEM(body)
	if err != nil {
		return nil, fmt.Errorf("mint: %s key: %w", m.Alg, err)
	}
	switch m.Alg {
	case MintRS256:
		if _, ok := priv.(*rsa.PrivateKey); !ok {
			return nil, fmt.Errorf("mint: RS256 needs an RSA private key, got %T", priv)
		}
	case MintES256:
		ec, ok := priv.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("mint: ES256 needs an ECDSA private key, got %T", priv)
		}
		if ec.Curve != elliptic.P256() {
			return nil, fmt.Errorf("mint: ES256 mandates the P-256 curve, got %s", ec.Curve.Params().Name)
		}
	}
	// Hold the PEM bytes; the signer re-parses them at sign time (a single parse per
	// provider is fine — the provider parses once on build, see auth.NewMintProvider).
	return body, nil
}

// parsePrivateKeyPEM decodes a PEM-wrapped private key in any of the common encodings
// (PKCS#8, PKCS#1 RSA, SEC1 EC), returning the parsed key. It is the single PEM entry
// point both DecodeKey (type/curve check) and the signer (re-parse to sign) use, so
// the two agree on what a "PEM private key" means.
func parsePrivateKeyPEM(body []byte) (any, error) {
	block, _ := pem.Decode(body)
	if block == nil {
		return nil, fmt.Errorf("not a PEM block")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("unrecognized PEM private key (want PKCS#8, PKCS#1 or SEC1 EC)")
}
