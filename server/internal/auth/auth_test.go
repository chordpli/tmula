package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

func TestPoolProviderRoundRobin(t *testing.T) {
	p, err := NewPoolProvider([]domain.Credential{{Subject: "u0"}, {Subject: "u1"}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	want := []string{"u0", "u1", "u0", "u1", "u0"}
	for i, w := range want {
		c, err := p.Acquire(context.Background(), i)
		if err != nil || c.Subject != w {
			t.Errorf("Acquire(%d) = %q, %v; want %q", i, c.Subject, err, w)
		}
	}
}

func TestPoolProviderEmpty(t *testing.T) {
	if _, err := NewPoolProvider(nil); err == nil {
		t.Fatal("empty pool should error")
	}
}

func TestBootstrapSignupCachesPerUser(t *testing.T) {
	var mu sync.Mutex
	calls := map[int]int{}
	signup := func(_ context.Context, i int) (domain.Credential, error) {
		mu.Lock()
		calls[i]++
		mu.Unlock()
		return domain.Credential{Subject: fmt.Sprintf("user-%d", i), Secret: "tok"}, nil
	}
	b, err := NewBootstrapSignupProvider(signup)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	c1, _ := b.Acquire(context.Background(), 0)
	c2, _ := b.Acquire(context.Background(), 0)
	if c1.Subject != "user-0" || c2.Subject != "user-0" {
		t.Fatalf("unexpected subjects: %q %q", c1.Subject, c2.Subject)
	}
	if calls[0] != 1 {
		t.Errorf("signup for user 0 ran %d times, want 1 (cached)", calls[0])
	}
	if _, _ = b.Acquire(context.Background(), 1); calls[1] != 1 {
		t.Errorf("signup for user 1 ran %d times, want 1", calls[1])
	}
}

func TestBootstrapPrewarm(t *testing.T) {
	var n int
	var mu sync.Mutex
	signup := func(_ context.Context, i int) (domain.Credential, error) {
		mu.Lock()
		n++
		mu.Unlock()
		return domain.Credential{Subject: fmt.Sprintf("u%d", i)}, nil
	}
	b, _ := NewBootstrapSignupProvider(signup)
	if err := b.Prewarm(context.Background(), 5); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	if n != 5 {
		t.Errorf("prewarm ran %d signups, want 5", n)
	}
	// Already cached: a later Acquire does not sign up again.
	_, _ = b.Acquire(context.Background(), 3)
	if n != 5 {
		t.Errorf("acquire after prewarm signed up again: %d", n)
	}
}

func TestBootstrapSignupError(t *testing.T) {
	signup := func(context.Context, int) (domain.Credential, error) {
		return domain.Credential{}, errors.New("signup down")
	}
	b, _ := NewBootstrapSignupProvider(signup)
	if _, err := b.Acquire(context.Background(), 0); err == nil {
		t.Fatal("expected signup error to propagate")
	}
}

func TestNewProviderFactory(t *testing.T) {
	pool := domain.CredentialPool{Strategy: domain.CredPool, Entries: []domain.Credential{{Subject: "a"}}}
	if p, err := NewProvider(pool, nil); err != nil {
		t.Errorf("pool provider: %v", err)
	} else if _, ok := p.(*PoolProvider); !ok {
		t.Errorf("expected *PoolProvider, got %T", p)
	}

	signup := func(context.Context, int) (domain.Credential, error) { return domain.Credential{}, nil }
	boot := domain.CredentialPool{Strategy: domain.CredBootstrapSignup}
	if p, err := NewProvider(boot, signup); err != nil {
		t.Errorf("bootstrap provider: %v", err)
	} else if _, ok := p.(*BootstrapSignupProvider); !ok {
		t.Errorf("expected *BootstrapSignupProvider, got %T", p)
	}

	if _, err := NewProvider(boot, nil); err == nil {
		t.Error("bootstrap strategy without signup func should error")
	}
}
