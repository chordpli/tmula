// Package auth supplies credentials to virtual users. Two strategies ship: a
// pre-supplied pool (inject existing JWT/session/member info) and bootstrap
// signup (register one account per virtual user up front). Both satisfy the
// same Provider interface so OAuth or other schemes can be added later.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

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

// PoolProvider hands out pre-supplied credentials, one per virtual user,
// wrapping around if there are more users than credentials.
type PoolProvider struct {
	entries []domain.Credential
}

// NewPoolProvider builds a pool provider from pre-supplied credentials.
func NewPoolProvider(entries []domain.Credential) (*PoolProvider, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("auth: pool provider needs at least one credential")
	}
	return &PoolProvider{entries: entries}, nil
}

// Acquire returns the credential assigned to userIndex.
func (p *PoolProvider) Acquire(_ context.Context, userIndex int) (domain.Credential, error) {
	if userIndex < 0 {
		return domain.Credential{}, fmt.Errorf("auth: negative user index %d", userIndex)
	}
	return p.entries[userIndex%len(p.entries)], nil
}

// SignupFunc registers one account and returns its credential. It is injected
// so the bootstrap provider is independent of any concrete signup transport.
type SignupFunc func(ctx context.Context, userIndex int) (domain.Credential, error)

// TeardownFunc deprovisions one provisioned account, identified by the same
// userIndex Acquire keyed it under and carrying the credential the signup captured
// (so the teardown journey can template the account's subject). It is injected so
// the bootstrap provider is independent of any concrete teardown transport.
type TeardownFunc func(ctx context.Context, userIndex int, cred domain.Credential) error

// signupCall tracks one in-flight signup so concurrent Acquire calls for the
// same user share a single signup instead of all serializing on one lock.
type signupCall struct {
	done chan struct{}
	cred domain.Credential
	err  error
}

// BootstrapSignupProvider provisions an account per virtual user by running a
// signup up front, caching the result so each user keeps a stable identity.
type BootstrapSignupProvider struct {
	signup   SignupFunc
	teardown TeardownFunc
	mu       sync.Mutex
	cache    map[int]domain.Credential
	inflight map[int]*signupCall
}

// NewBootstrapSignupProvider builds a provider that signs up accounts on demand.
func NewBootstrapSignupProvider(signup SignupFunc) (*BootstrapSignupProvider, error) {
	if signup == nil {
		return nil, fmt.Errorf("auth: bootstrap provider needs a signup function")
	}
	return &BootstrapSignupProvider{
		signup:   signup,
		cache:    make(map[int]domain.Credential),
		inflight: make(map[int]*signupCall),
	}, nil
}

// Acquire returns the credential for userIndex, signing up (once) if needed.
// The signup runs without holding the lock, so different users sign up in
// parallel and concurrent callers for the same user share one signup. A failed
// signup is not cached, so it can be retried.
func (b *BootstrapSignupProvider) Acquire(ctx context.Context, userIndex int) (domain.Credential, error) {
	b.mu.Lock()
	if c, ok := b.cache[userIndex]; ok {
		b.mu.Unlock()
		return c, nil
	}
	if call, ok := b.inflight[userIndex]; ok {
		b.mu.Unlock()
		<-call.done
		return call.cred, call.err
	}
	call := &signupCall{done: make(chan struct{})}
	b.inflight[userIndex] = call
	b.mu.Unlock()

	cred, err := b.signup(ctx, userIndex)

	b.mu.Lock()
	if err != nil {
		call.err = fmt.Errorf("auth: signup user %d: %w", userIndex, err)
	} else {
		call.cred = cred
		b.cache[userIndex] = cred
	}
	delete(b.inflight, userIndex)
	b.mu.Unlock()
	close(call.done)
	return call.cred, call.err
}

// Prewarm runs the bootstrap signup phase for n users ahead of the run.
func (b *BootstrapSignupProvider) Prewarm(ctx context.Context, n int) error {
	for i := 0; i < n; i++ {
		if _, err := b.Acquire(ctx, i); err != nil {
			return err
		}
	}
	return nil
}

// SetTeardown installs the teardown transport the provider deprovisions accounts
// through. It is set once at build time (the orchestrator wires it from the pool's
// teardown flow), before Teardown is ever called, so it needs no lock. A nil
// teardown makes Teardown a cache-clearing no-op — the --keep-accounts and
// no-teardown-wired cases.
func (b *BootstrapSignupProvider) SetTeardown(teardown TeardownFunc) {
	b.teardown = teardown
}

// Teardown deprovisions every account the provider provisioned, walking each cached
// identity through the teardown transport, and then clears the cache. It is the
// gating-safety counterpart to bootstrap provisioning: after a run, the real
// accounts a bootstrap pool created are removed so a load test does not strand
// thousands of them.
//
// It is BEST-EFFORT and NON-ABORTING: a failure tearing down one account does not
// stop the others — every account is attempted, each orphan is logged at ERROR
// (alertable: a real account leaked), and the failures are returned as a single
// aggregated error for the caller to surface WITHOUT failing the run. The cache is
// cleared regardless of partial failure, so a second Teardown is a no-op (the
// orchestrator defers it, and a manual retry must not double-delete the accounts
// that did succeed). It is IDEMPOTENT for the same reason: once the cache is empty
// there is nothing left to tear down.
//
// Accounts are torn down in ascending index order for deterministic logs. The
// teardown runs under the context the caller passes; the orchestrator passes a
// FRESH context.Background() (not the run context) with a scaled timeout, so a
// killed or timed-out run still deprovisions.
func (b *BootstrapSignupProvider) Teardown(ctx context.Context) error {
	b.mu.Lock()
	indices := make([]int, 0, len(b.cache))
	for i := range b.cache {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	creds := make(map[int]domain.Credential, len(b.cache))
	for _, i := range indices {
		creds[i] = b.cache[i]
	}
	// Clear the cache up front and unconditionally: a second Teardown (the deferred
	// one, or a manual retry) must never re-attempt the accounts handled here, even
	// if some of them failed — re-deleting a succeeded account is its own hazard.
	b.cache = make(map[int]domain.Credential)
	b.mu.Unlock()

	if b.teardown == nil {
		return nil
	}

	var errs []error
	for _, i := range indices {
		if err := b.teardown(ctx, i, creds[i]); err != nil {
			// An orphaned real account: log at ERROR so it is alertable, then keep
			// going — one failure must not strand the rest.
			slog.Error("bootstrap teardown left an orphaned account",
				"userIndex", i, "subject", creds[i].Subject, "err", err)
			errs = append(errs, fmt.Errorf("auth: teardown account %d (subject %q): %w", i, creds[i].Subject, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("auth: %d of %d accounts failed teardown (orphaned): %w", len(errs), len(indices), errors.Join(errs...))
	}
	return nil
}

// TokenFunc mints one principal's token by running a login flow and returns its
// credential. It is injected so the login provider is independent of any concrete
// login transport (the transport lives a layer above, in the api package, and is
// compiled there). userIndex keys the principal so each virtual user logs in as a
// distinct account.
type TokenFunc func(ctx context.Context, userIndex int) (domain.Credential, error)

// LoginProvider mints a token per virtual user by running a login flow up front,
// caching the result so each user keeps a stable identity for the run. It is a
// near-clone of BootstrapSignupProvider: cache-by-index, in-flight dedup so
// concurrent Acquire calls for the same user share one login, and a failed mint is
// not cached so it can be retried. It composes ABOVE the Provider seam — the
// static PoolProvider path is untouched.
type LoginProvider struct {
	token    TokenFunc
	mu       sync.Mutex
	cache    map[int]domain.Credential
	inflight map[int]*signupCall
}

// NewLoginProvider builds a provider that mints tokens on demand via token.
func NewLoginProvider(token TokenFunc) (*LoginProvider, error) {
	if token == nil {
		return nil, fmt.Errorf("auth: login provider needs a token function")
	}
	return &LoginProvider{
		token:    token,
		cache:    make(map[int]domain.Credential),
		inflight: make(map[int]*signupCall),
	}, nil
}

// Acquire returns the credential for userIndex, minting one (once) if needed.
// The login runs without holding the lock, so different users log in parallel and
// concurrent callers for the same user share one login. A failed login is not
// cached, so it can be retried.
func (l *LoginProvider) Acquire(ctx context.Context, userIndex int) (domain.Credential, error) {
	l.mu.Lock()
	if c, ok := l.cache[userIndex]; ok {
		l.mu.Unlock()
		return c, nil
	}
	if call, ok := l.inflight[userIndex]; ok {
		l.mu.Unlock()
		<-call.done
		return call.cred, call.err
	}
	call := &signupCall{done: make(chan struct{})}
	l.inflight[userIndex] = call
	l.mu.Unlock()

	cred, err := l.token(ctx, userIndex)

	l.mu.Lock()
	if err != nil {
		call.err = fmt.Errorf("auth: login user %d: %w", userIndex, err)
	} else {
		call.cred = cred
		l.cache[userIndex] = cred
	}
	delete(l.inflight, userIndex)
	l.mu.Unlock()
	close(call.done)
	return call.cred, call.err
}

// Set replaces the cached credential for userIndex. It is how a mid-run refresh
// records the freshly minted token so a later Acquire serves the fresh one.
func (l *LoginProvider) Set(userIndex int, cred domain.Credential) {
	l.mu.Lock()
	l.cache[userIndex] = cred
	l.mu.Unlock()
}

// Refresh re-runs the login flow for userIndex, replaces the cached credential
// with the freshly minted one, and returns it. Unlike Acquire it does NOT serve a
// cached value — it always mints anew — which is the mid-run 401 recovery path
// (reactive re-acquire of the same principal). A failed refresh leaves the
// existing cache untouched and is returned to the caller.
func (l *LoginProvider) Refresh(ctx context.Context, userIndex int) (domain.Credential, error) {
	cred, err := l.token(ctx, userIndex)
	if err != nil {
		return domain.Credential{}, fmt.Errorf("auth: refresh login user %d: %w", userIndex, err)
	}
	l.Set(userIndex, cred)
	return cred, nil
}

// Prewarm mints tokens for n users ahead of the run.
func (l *LoginProvider) Prewarm(ctx context.Context, n int) error {
	for i := 0; i < n; i++ {
		if _, err := l.Acquire(ctx, i); err != nil {
			return err
		}
	}
	return nil
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
	// Teardown deprovisions a provisioned account (bootstrap-signup strategy). It is
	// optional: a nil Teardown makes the provider's Teardown a cache-clearing no-op
	// (the --keep-accounts path). The run path requires either a teardown or an
	// explicit keep-accounts opt-out before a bootstrap run is accepted.
	Teardown TeardownFunc
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
		return NewLoginProvider(deps.Token)
	default:
		return nil, fmt.Errorf("auth: unknown credential strategy %q", pool.Strategy)
	}
}
