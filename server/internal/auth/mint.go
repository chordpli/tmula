package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// mint.go implements the CredMint strategy: SELF-ISSUE a JWT per virtual user by
// signing one LOCALLY with a key the operator holds (the M1 case). It SKIPS token
// acquisition entirely — no login, refresh or capture — so each VU gets a token
// instantly: render the claims template for the index, stamp iat/exp (and an
// optional nbf), sign a compact JWS (jws.go, standard library only), and hand back
// a Credential{Subject, Secret}. Acquire is deterministic per index for a fixed
// clock, so a reproduce replays the same principal with no extra wiring.

// nowFunc is the clock the provider stamps iat/exp from. It is injected so a test
// can fix time and assert exp = now+ttl; the run path passes time.Now.
type nowFunc func() time.Time

// MintProvider self-issues a signed JWT per virtual user. It holds the resolved
// signing key (in-process only — the key reference, never the key, crosses the
// wire) and the claims/subject templates, and signs a fresh token on each Acquire.
type MintProvider struct {
	spec    domain.MintSpec
	key     []byte
	now     nowFunc
	subject *template.Template            // nil when MintSpec.Subject is empty
	claims  map[string]*template.Template // pre-parsed claim value templates
}

// NewMintProvider builds a mint provider from a validated spec, its resolved signing
// key (the in-process raw bytes — the decoded HMAC secret for HS256, or the PEM bytes
// for RS256/ES256), and a clock. It pre-parses the subject and claim templates so a
// per-Acquire render is a cheap execute, and rejects a spec with no key (a mint run
// with an unresolved key reference is a wiring bug, not a silent anonymous run). now
// may be nil, defaulting to time.Now.
func NewMintProvider(spec domain.MintSpec, key []byte, now nowFunc) (*MintProvider, error) {
	// The key reference has already done its job (the caller resolved it into key),
	// so validate the RESOLVED spec — a signable alg and a positive TTL — not the
	// reference shape (which the authoring/wire path's MintSpec.Validate covers).
	if err := spec.ValidateResolved(); err != nil {
		return nil, err
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("auth: mint provider needs a resolved signing key (the key reference must be resolved in-process first)")
	}
	if now == nil {
		now = time.Now
	}
	p := &MintProvider{spec: spec, key: key, now: now, claims: make(map[string]*template.Template, len(spec.Claims))}
	if strings.TrimSpace(spec.Subject) != "" {
		t, err := parseClaimTemplate("mint", "subject", spec.Subject)
		if err != nil {
			return nil, err
		}
		p.subject = t
	}
	for name, tmpl := range spec.Claims {
		t, err := parseClaimTemplate("mint", "claim "+name, tmpl)
		if err != nil {
			return nil, err
		}
		p.claims[name] = t
	}
	return p, nil
}

// Acquire mints a signed JWT for userIndex: render the subject and claims for the
// index, stamp iat/exp (and an optional nbf when a leeway is set), and sign a compact
// JWS. The returned Credential carries the rendered subject (non-sensitive, recorded
// for evidence) and the signed token as Secret (json:"-"). It is deterministic for a
// fixed clock — Acquire(i) signs the same bytes every call — so a reproduce replays
// the same principal.
func (p *MintProvider) Acquire(_ context.Context, userIndex int) (domain.Credential, error) {
	if userIndex < 0 {
		return domain.Credential{}, fmt.Errorf("auth: mint negative user index %d", userIndex)
	}
	// The subject renders first (only userIndex is in scope), so it can feed the
	// claims render below as {{.subject}}.
	subjectData := map[string]string{"userIndex": strconv.Itoa(userIndex)}
	subject := ""
	if p.subject != nil {
		s, err := execTemplate(p.subject, subjectData)
		if err != nil {
			return domain.Credential{}, fmt.Errorf("auth: mint user %d: render subject: %w", userIndex, err)
		}
		subject = s
	}

	now := p.now()
	claims := map[string]any{
		"iat": now.Unix(),
		"exp": now.Add(p.spec.TTL).Unix(),
	}
	if subject != "" {
		claims["sub"] = subject
	}
	if p.spec.Leeway > 0 {
		claims["nbf"] = now.Add(-p.spec.Leeway).Unix()
	}
	// Custom claims render with both userIndex and the resolved subject in scope, so a
	// claim can key off the same principal the sub does.
	claimData := map[string]string{"userIndex": strconv.Itoa(userIndex), "subject": subject}
	for name, tmpl := range p.claims {
		v, err := execTemplate(tmpl, claimData)
		if err != nil {
			return domain.Credential{}, fmt.Errorf("auth: mint user %d: render claim %q: %w", userIndex, name, err)
		}
		// A reserved standard claim a custom template sets wins over the stamped one,
		// so an operator can override exp/sub deliberately; the common case is a fresh
		// claim name that simply adds to the set. RFC 7519 NumericDate claims must be
		// JSON numbers — a string "1735689600" is rejected by real verifiers — so an
		// overridden exp/nbf/iat is parsed to a number (and fails loudly otherwise).
		if isNumericDateClaim(name) {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return domain.Credential{}, fmt.Errorf("auth: mint user %d: claim %q must render to a whole number of seconds since epoch (got %q)", userIndex, name, v)
			}
			claims[name] = n
			continue
		}
		claims[name] = v
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return domain.Credential{}, fmt.Errorf("auth: mint user %d: marshal claims: %w", userIndex, err)
	}
	token, err := SignCompact(p.spec.Alg, p.key, payload)
	if err != nil {
		return domain.Credential{}, fmt.Errorf("auth: mint user %d: %w", userIndex, err)
	}
	return domain.Credential{Subject: subject, Secret: token}, nil
}

// isNumericDateClaim reports whether a claim name is an RFC 7519 NumericDate
// (seconds since epoch) that must serialize as a JSON number.
func isNumericDateClaim(name string) bool {
	switch name {
	case "exp", "nbf", "iat":
		return true
	}
	return false
}

// parseClaimTemplate parses one claim/subject template under missingkey=error, the
// same strict mode the request renderer uses, so a typo'd {{.userInxed}} fails loudly
// at build time rather than silently emitting "<no value>". label prefixes the error
// with the strategy the template belongs to ("mint" or "usersPattern") so a parse
// failure names the block the operator authored it in — the same helper serves both.
func parseClaimTemplate(label, name, text string) (*template.Template, error) {
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return nil, fmt.Errorf("auth: %s %s template: %w", label, name, err)
	}
	return t, nil
}

// execTemplate runs a parsed claim template against data.
func execTemplate(t *template.Template, data map[string]string) (string, error) {
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}
