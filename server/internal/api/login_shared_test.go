package api

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// sharedLoginAuth builds a loginAuth over a token func that counts mints, for the
// given scope. It bypasses the httptest layer to assert the holder-sharing
// invariant directly on the seam.
func sharedLoginAuth(t *testing.T, shared bool, mints *int64) *loginAuth {
	t.Helper()
	la, err := newLoginAuthFromToken(func(_ context.Context, idx int) (domain.Credential, error) {
		n := atomic.AddInt64(mints, 1)
		return domain.Credential{Subject: "p", Secret: "tok-" + itoa(int(n))}, nil
	}, nil, shared)
	if err != nil {
		t.Fatalf("build loginAuth: %v", err)
	}
	return la
}

// TestSharedScopeMintsOnceAndSharesHolder pins the shared (client_credentials)
// invariant: every session is handed the SAME holder pointer, and only ONE token
// is minted no matter how many users seed.
func TestSharedScopeMintsOnceAndSharesHolder(t *testing.T) {
	var mints int64
	la := sharedLoginAuth(t, true, &mints)

	h0, r0, err := la.seed(context.Background(), 0)
	if err != nil {
		t.Fatalf("seed 0: %v", err)
	}
	h1, _, err := la.seed(context.Background(), 1)
	if err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	h2, _, err := la.seed(context.Background(), 7)
	if err != nil {
		t.Fatalf("seed 7: %v", err)
	}

	// One token minted for all three sessions.
	if got := atomic.LoadInt64(&mints); got != 1 {
		t.Errorf("shared scope minted %d tokens, want 1", got)
	}
	// The SAME holder pointer is handed to every session (shared, not copied).
	if h0 != h1 || h1 != h2 {
		t.Error("shared scope handed different holders to different sessions; must share one pointer")
	}
	// A refresh through one session's closure is visible to every session, because
	// they share one holder. Rotate via r0 and confirm h1/h2 observe it.
	if err := r0(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if h1.Get().Secret != h0.Get().Secret || h2.Get().Secret != h0.Get().Secret {
		t.Error("a shared-holder refresh was not visible to every session")
	}
}

// TestPerUserScopeMintsPerUserDistinctHolders confirms per-user is unchanged: each
// session gets its own holder and its own minted token.
func TestPerUserScopeMintsPerUserDistinctHolders(t *testing.T) {
	var mints int64
	la := sharedLoginAuth(t, false, &mints)

	h0, _, err := la.seed(context.Background(), 0)
	if err != nil {
		t.Fatalf("seed 0: %v", err)
	}
	h1, _, err := la.seed(context.Background(), 1)
	if err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if h0 == h1 {
		t.Error("per-user scope shared a holder across users; each must get its own")
	}
	if got := atomic.LoadInt64(&mints); got != 2 {
		t.Errorf("per-user scope minted %d tokens, want 2 (one per user)", got)
	}
	if h0.Get().Secret == h1.Get().Secret {
		t.Errorf("per-user users got the same token %q; expected distinct mints", h0.Get().Secret)
	}
}
