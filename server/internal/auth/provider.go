// Package auth supplies credentials to virtual users. Two strategies ship: a
// pre-supplied pool (inject existing JWT/session/member info) and bootstrap
// signup (register one account per virtual user up front). Both satisfy the
// same Provider interface so OAuth or other schemes can be added later.
package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// Provider supplies a credential for a virtual user. Credential secrets carry a
// json:"-" tag (domain), so persisting a credential never leaks the secret.
type Provider interface {
	Acquire(ctx context.Context, userIndex int) (domain.Credential, error)
}

// TearDowner deprovisions the principals a provider provisioned. Only the
// bootstrap-signup provider implements it (a pre-supplied pool owns no accounts);
// the orchestrator defers Teardown after a run so the real accounts a bootstrap run
// created do not leak. It is best-effort and idempotent — see
// BootstrapSignupProvider.Teardown.
type TearDowner interface {
	Teardown(ctx context.Context) error
}

// ProviderDeps carries the per-strategy functions NewProvider injects so the
// signature does not grow a nil-able positional argument per strategy. A plain
// pool needs none of them; bootstrap-signup needs Signup; login needs Token. A
// later phase adds Teardown to the same struct.
type ProviderDeps struct {
	// Signup mints a fresh account per virtual user (bootstrap-signup strategy).
	Signup SignupFunc
	// Token mints a token per virtual user by running a login flow (login strategy).
	Token TokenFunc
	// RefreshToken rotates a login credential via a real grant_type=refresh_token
	// exchange (login strategy). It is OPTIONAL: a nil RefreshToken keeps the login
	// provider's Refresh on the re-login path — the safe fallback for a login flow that
	// cannot be expressed as a refresh-token grant.
	RefreshToken RefreshTokenFunc
	// Teardown deprovisions a provisioned account (bootstrap-signup strategy). It is
	// optional: a nil Teardown makes the provider's Teardown a cache-clearing no-op
	// (the --keep-accounts path). The run path requires either a teardown or an
	// explicit keep-accounts opt-out before a bootstrap run is accepted.
	Teardown TeardownFunc
	// MintKey is the resolved in-process signing key for the mint strategy — the
	// decoded HMAC secret (HS256) or the PEM private-key bytes (RS256/ES256). It is
	// resolved from the pool's MintSpec.Key reference by the layer that builds the
	// provider (runspec.CredentialProvider, the same layer pool entries resolve at),
	// and held only in process: the key reference, never the key, crosses the wire.
	MintKey []byte
	// Now is the clock the mint provider stamps iat/exp from. Optional — a nil Now
	// defaults to time.Now; it is injected only so a test can fix time.
	Now func() time.Time
}

// NewProvider selects a provider for a credential pool from the injected deps. A
// plain pool requires entries; bootstrap-signup requires deps.Signup; login
// requires deps.Token.
func NewProvider(pool domain.CredentialPool, deps ProviderDeps) (Provider, error) {
	switch pool.Strategy {
	case domain.CredPool:
		return NewPoolProvider(pool.Entries)
	case domain.CredBootstrapSignup:
		p, err := NewBootstrapSignupProvider(deps.Signup)
		if err != nil {
			return nil, err
		}
		p.SetTeardown(deps.Teardown)
		return p, nil
	case domain.CredLogin:
		p, err := NewLoginProvider(deps.Token)
		if err != nil {
			return nil, err
		}
		// Nil-safe: an unset RefreshToken keeps Refresh on the re-login fallback path.
		p.SetRefreshToken(deps.RefreshToken)
		return p, nil
	case domain.CredMint:
		// The mint strategy self-issues a JWT per virtual user; it needs no token/
		// signup transport — only the resolved signing key (deps.MintKey), resolved
		// from pool.Mint.Key by the layer that built this provider. A nil Mint or an
		// empty key is rejected (a mint run with no key is a wiring bug).
		if pool.Mint == nil {
			return nil, fmt.Errorf("auth: mint strategy needs a mint spec on the pool")
		}
		return NewMintProvider(*pool.Mint, deps.MintKey, deps.Now)
	case domain.CredExec:
		// The exec strategy runs an operator-supplied command per virtual user and uses
		// its stdout as the token; it needs no flow/transport, so it builds RIGHT HERE
		// over an exec-backed TokenFunc wrapped in a LoginProvider — cache-by-index,
		// in-flight dedup and Refresh all come for free, and Refresh simply re-runs the
		// command (no refresh transport is wired). A nil Exec spec is a wiring bug, not a
		// silent anonymous run.
		if pool.Exec == nil {
			return nil, fmt.Errorf("auth: exec strategy needs an exec spec on the pool")
		}
		return NewLoginProvider(NewExecTokenFunc(*pool.Exec))
	default:
		return nil, fmt.Errorf("auth: unknown credential strategy %q", pool.Strategy)
	}
}
