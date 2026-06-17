package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	if p, err := NewProvider(pool, ProviderDeps{}); err != nil {
		t.Errorf("pool provider: %v", err)
	} else if _, ok := p.(*PoolProvider); !ok {
		t.Errorf("expected *PoolProvider, got %T", p)
	}

	signup := func(context.Context, int) (domain.Credential, error) { return domain.Credential{}, nil }
	boot := domain.CredentialPool{Strategy: domain.CredBootstrapSignup}
	if p, err := NewProvider(boot, ProviderDeps{Signup: signup}); err != nil {
		t.Errorf("bootstrap provider: %v", err)
	} else if _, ok := p.(*BootstrapSignupProvider); !ok {
		t.Errorf("expected *BootstrapSignupProvider, got %T", p)
	}

	if _, err := NewProvider(boot, ProviderDeps{}); err == nil {
		t.Error("bootstrap strategy without signup func should error")
	}

	// Login strategy: needs a token func; produces a *LoginProvider.
	token := func(context.Context, int) (domain.Credential, error) { return domain.Credential{}, nil }
	login := domain.CredentialPool{Strategy: domain.CredLogin}
	if p, err := NewProvider(login, ProviderDeps{Token: token}); err != nil {
		t.Errorf("login provider: %v", err)
	} else if _, ok := p.(*LoginProvider); !ok {
		t.Errorf("expected *LoginProvider, got %T", p)
	}
	if _, err := NewProvider(login, ProviderDeps{}); err == nil {
		t.Error("login strategy without token func should error")
	}
}

func TestLoginProviderCachesPerUser(t *testing.T) {
	var mu sync.Mutex
	calls := map[int]int{}
	token := func(_ context.Context, i int) (domain.Credential, error) {
		mu.Lock()
		calls[i]++
		mu.Unlock()
		return domain.Credential{Subject: fmt.Sprintf("user-%d", i), Secret: fmt.Sprintf("tok-%d", i)}, nil
	}
	p, err := NewLoginProvider(token)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	c1, _ := p.Acquire(context.Background(), 0)
	c2, _ := p.Acquire(context.Background(), 0)
	if c1.Secret != "tok-0" || c2.Secret != "tok-0" {
		t.Fatalf("unexpected tokens: %q %q", c1.Secret, c2.Secret)
	}
	if calls[0] != 1 {
		t.Errorf("token mint for user 0 ran %d times, want 1 (cached)", calls[0])
	}
	if _, _ = p.Acquire(context.Background(), 1); calls[1] != 1 {
		t.Errorf("token mint for user 1 ran %d times, want 1", calls[1])
	}
}

func TestLoginProviderDedupesConcurrent(t *testing.T) {
	var calls int64
	release := make(chan struct{})
	token := func(_ context.Context, i int) (domain.Credential, error) {
		atomic.AddInt64(&calls, 1)
		<-release // hold every in-flight mint until the test releases it
		return domain.Credential{Subject: fmt.Sprintf("u%d", i), Secret: "tok"}, nil
	}
	p, _ := NewLoginProvider(token)

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Acquire(context.Background(), 0) // all for the SAME index
		}()
	}
	// Give the goroutines time to all reach the in-flight wait, then release.
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Errorf("concurrent Acquire for one index minted %d tokens, want 1 (deduped)", got)
	}
}

func TestLoginProviderError(t *testing.T) {
	token := func(context.Context, int) (domain.Credential, error) {
		return domain.Credential{}, errors.New("login down")
	}
	p, _ := NewLoginProvider(token)
	if _, err := p.Acquire(context.Background(), 0); err == nil {
		t.Fatal("expected login error to propagate")
	}
}

func TestLoginProviderRetriesAfterFailure(t *testing.T) {
	var n int
	token := func(context.Context, int) (domain.Credential, error) {
		n++
		if n == 1 {
			return domain.Credential{}, errors.New("transient")
		}
		return domain.Credential{Secret: "ok"}, nil
	}
	p, _ := NewLoginProvider(token)
	if _, err := p.Acquire(context.Background(), 0); err == nil {
		t.Fatal("first acquire should fail")
	}
	// A failed mint is not cached, so the next acquire retries and succeeds.
	c, err := p.Acquire(context.Background(), 0)
	if err != nil || c.Secret != "ok" {
		t.Fatalf("retry after failure = %q, %v; want a fresh successful mint", c.Secret, err)
	}
}

func TestLoginProviderPrewarm(t *testing.T) {
	var n int
	var mu sync.Mutex
	token := func(_ context.Context, i int) (domain.Credential, error) {
		mu.Lock()
		n++
		mu.Unlock()
		return domain.Credential{Subject: fmt.Sprintf("u%d", i)}, nil
	}
	p, _ := NewLoginProvider(token)
	if err := p.Prewarm(context.Background(), 5); err != nil {
		t.Fatalf("prewarm: %v", err)
	}
	if n != 5 {
		t.Errorf("prewarm minted %d tokens, want 5", n)
	}
	// Already cached: a later Acquire does not mint again.
	_, _ = p.Acquire(context.Background(), 3)
	if n != 5 {
		t.Errorf("acquire after prewarm minted again: %d", n)
	}
}

func TestLoginProviderNilTokenFunc(t *testing.T) {
	if _, err := NewLoginProvider(nil); err == nil {
		t.Fatal("login provider without a token func should error")
	}
}
