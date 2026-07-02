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
