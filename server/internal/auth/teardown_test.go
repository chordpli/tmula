package auth

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// provisionedProvider returns a bootstrap provider that has provisioned n accounts
// (indices 0..n-1) and the teardown recorder it was wired with.
func provisionedProvider(t *testing.T, n int, teardown TeardownFunc) *BootstrapSignupProvider {
	t.Helper()
	signup := func(_ context.Context, i int) (domain.Credential, error) {
		return domain.Credential{Subject: fmt.Sprintf("acct-%d", i), Secret: fmt.Sprintf("tok-%d", i)}, nil
	}
	b, err := NewBootstrapSignupProvider(signup)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	b.SetTeardown(teardown)
	for i := 0; i < n; i++ {
		if _, err := b.Acquire(context.Background(), i); err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
	}
	return b
}

// TestTeardownDeprovisionsEveryAccount confirms Teardown walks each cached identity
// through the teardown func exactly once, with the provisioned credential.
func TestTeardownDeprovisionsEveryAccount(t *testing.T) {
	var mu sync.Mutex
	torn := map[int]domain.Credential{}
	b := provisionedProvider(t, 3, func(_ context.Context, idx int, cred domain.Credential) error {
		mu.Lock()
		torn[idx] = cred
		mu.Unlock()
		return nil
	})

	if err := b.Teardown(context.Background()); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if len(torn) != 3 {
		t.Fatalf("torn down %d accounts, want 3", len(torn))
	}
	for i := 0; i < 3; i++ {
		if torn[i].Secret != fmt.Sprintf("tok-%d", i) {
			t.Errorf("account %d torn down with secret %q, want tok-%d", i, torn[i].Secret, i)
		}
	}
}

// TestTeardownIsIdempotent proves a second Teardown is a no-op: the cache is
// cleared by the first, so the teardown func is never called again.
func TestTeardownIsIdempotent(t *testing.T) {
	var calls int
	b := provisionedProvider(t, 2, func(_ context.Context, _ int, _ domain.Credential) error {
		calls++
		return nil
	})
	if err := b.Teardown(context.Background()); err != nil {
		t.Fatalf("teardown 1: %v", err)
	}
	if calls != 2 {
		t.Fatalf("first teardown called the func %d times, want 2", calls)
	}
	if err := b.Teardown(context.Background()); err != nil {
		t.Fatalf("teardown 2: %v", err)
	}
	if calls != 2 {
		t.Errorf("second teardown called the func again (total %d), want it to be a no-op", calls)
	}
}

// TestTeardownContinuesOnPartialFailure is critic must-have (a): account 1 deletes,
// account 2's teardown fails, so the rest still proceed, Teardown does NOT abort and
// returns an aggregated error naming the orphan, and the cache is still cleared (a
// second Teardown is a no-op even after a partial failure).
func TestTeardownContinuesOnPartialFailure(t *testing.T) {
	var mu sync.Mutex
	var attempted []int
	b := provisionedProvider(t, 3, func(_ context.Context, idx int, _ domain.Credential) error {
		mu.Lock()
		attempted = append(attempted, idx)
		mu.Unlock()
		if idx == 1 {
			return fmt.Errorf("DELETE 500")
		}
		return nil
	})

	err := b.Teardown(context.Background())
	if err == nil {
		t.Fatal("teardown with a failing account should return an aggregated error")
	}

	mu.Lock()
	sort.Ints(attempted)
	mu.Unlock()
	if len(attempted) != 3 {
		t.Fatalf("teardown attempted %d accounts, want all 3 despite the failure (no abort)", len(attempted))
	}

	// Cache cleared despite the partial failure: a second Teardown is a no-op.
	var second int
	b.SetTeardown(func(_ context.Context, _ int, _ domain.Credential) error {
		second++
		return nil
	})
	if err := b.Teardown(context.Background()); err != nil {
		t.Fatalf("second teardown after partial failure: %v", err)
	}
	if second != 0 {
		t.Errorf("cache not cleared after partial failure: second teardown ran %d times, want 0", second)
	}
}

// TestTeardownWithoutFuncIsNoop proves a provider with no teardown func clears its
// cache without error (the --keep-accounts / no-teardown-wired case never panics).
func TestTeardownWithoutFuncIsNoop(t *testing.T) {
	b := provisionedProvider(t, 2, nil)
	if err := b.Teardown(context.Background()); err != nil {
		t.Fatalf("teardown with no func: %v", err)
	}
}

// TestBootstrapProviderSatisfiesTearDowner pins the interface so the orchestrator can
// defer Teardown through it.
func TestBootstrapProviderSatisfiesTearDowner(t *testing.T) {
	var _ TearDowner = provisionedProvider(t, 1, nil)
}
