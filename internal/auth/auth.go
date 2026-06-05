// Package auth supplies credentials to virtual users. Two strategies ship: a
// pre-supplied pool (inject existing JWT/session/member info) and bootstrap
// signup (register one account per virtual user up front). Both satisfy the
// same Provider interface so OAuth or other schemes can be added later.
package auth

import (
	"context"
	"fmt"
	"sync"

	"github.com/chordpli/tmula/internal/domain"
)

// Provider supplies a credential for a virtual user. Credential secrets carry a
// json:"-" tag (domain), so persisting a credential never leaks the secret.
type Provider interface {
	Acquire(ctx context.Context, userIndex int) (domain.Credential, error)
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

// NewProvider selects a provider for a credential pool. A bootstrap-signup pool
// requires a signup function; a plain pool requires entries.
func NewProvider(pool domain.CredentialPool, signup SignupFunc) (Provider, error) {
	switch pool.Strategy {
	case domain.CredPool:
		return NewPoolProvider(pool.Entries)
	case domain.CredBootstrapSignup:
		return NewBootstrapSignupProvider(signup)
	default:
		return nil, fmt.Errorf("auth: unknown credential strategy %q", pool.Strategy)
	}
}
