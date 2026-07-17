package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestRefreshNilFuncFallsBackToToken pins test 1: with no refreshToken func wired,
// Refresh re-runs the login (token) path exactly as before — no regression for a
// login flow that cannot be expressed as a grant_type=refresh_token exchange.
func TestRefreshNilFuncFallsBackToToken(t *testing.T) {
	var tokenCalls int64
	token := func(_ context.Context, i int) (domain.Credential, error) {
		atomic.AddInt64(&tokenCalls, 1)
		return domain.Credential{Subject: "u", Secret: "relogged"}, nil
	}
	p, err := NewLoginProvider(token)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// No SetRefreshToken: refreshToken is nil.
	cred, err := p.Refresh(context.Background(), 0)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if cred.Secret != "relogged" {
		t.Errorf("nil refreshToken func should re-login; secret = %q, want relogged", cred.Secret)
	}
	if got := atomic.LoadInt64(&tokenCalls); got != 1 {
		t.Errorf("token (re-login) called %d times, want 1", got)
	}
}

// TestRefreshNoCapturedRefreshTokenFallsBack pins test 2: a refreshToken func IS
// wired, but the current cached credential carries no refresh token (cur.Refresh
// == ""), so Refresh falls back to the login (token) path rather than attempting a
// grant_type=refresh_token exchange with an empty token.
func TestRefreshNoCapturedRefreshTokenFallsBack(t *testing.T) {
	var tokenCalls, refreshCalls int64
	token := func(_ context.Context, i int) (domain.Credential, error) {
		atomic.AddInt64(&tokenCalls, 1)
		return domain.Credential{Secret: "relogged"}, nil // no Refresh captured
	}
	p, _ := NewLoginProvider(token)
	p.SetRefreshToken(func(_ context.Context, _ int, _ domain.Credential) (domain.Credential, error) {
		atomic.AddInt64(&refreshCalls, 1)
		return domain.Credential{Secret: "exchanged"}, nil
	})
	// Seed a cached credential with NO refresh token.
	if _, err := p.Acquire(context.Background(), 0); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	atomic.StoreInt64(&tokenCalls, 0) // reset: count only the refresh path

	cred, err := p.Refresh(context.Background(), 0)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if cred.Secret != "relogged" {
		t.Errorf("empty cur.Refresh should fall back to re-login; secret = %q, want relogged", cred.Secret)
	}
	if got := atomic.LoadInt64(&refreshCalls); got != 0 {
		t.Errorf("refreshToken func called %d times with no captured refresh token, want 0", got)
	}
	if got := atomic.LoadInt64(&tokenCalls); got != 1 {
		t.Errorf("token (re-login) fallback called %d times, want 1", got)
	}
}

// TestRefreshUsesRefreshTokenFunc pins test 3: when the cached credential carries a
// refresh token, Refresh calls refreshToken with the CURRENT credential, writes the
// result via Set, and does NOT call the login (token) path.
func TestRefreshUsesRefreshTokenFunc(t *testing.T) {
	var tokenCalls int64
	token := func(_ context.Context, i int) (domain.Credential, error) {
		atomic.AddInt64(&tokenCalls, 1)
		return domain.Credential{Subject: "u", Secret: "access-1", Refresh: "refresh-1"}, nil
	}
	p, _ := NewLoginProvider(token)

	var gotCur domain.Credential
	p.SetRefreshToken(func(_ context.Context, idx int, cur domain.Credential) (domain.Credential, error) {
		gotCur = cur
		if idx != 0 {
			t.Errorf("refreshToken got userIndex %d, want 0", idx)
		}
		return domain.Credential{Subject: cur.Subject, Secret: "access-2", Refresh: "refresh-2"}, nil
	})

	if _, err := p.Acquire(context.Background(), 0); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	atomic.StoreInt64(&tokenCalls, 0) // only count re-login during refresh

	cred, err := p.Refresh(context.Background(), 0)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if gotCur.Refresh != "refresh-1" {
		t.Errorf("refreshToken received cur.Refresh = %q, want refresh-1 (the current credential)", gotCur.Refresh)
	}
	if cred.Secret != "access-2" {
		t.Errorf("refresh result secret = %q, want access-2 (the exchanged access token)", cred.Secret)
	}
	if got := atomic.LoadInt64(&tokenCalls); got != 0 {
		t.Errorf("login (token) path called %d times during a refresh-token exchange, want 0", got)
	}
	// The rotated credential is written to the cache.
	post, _ := p.Acquire(context.Background(), 0)
	if post.Secret != "access-2" {
		t.Errorf("cache after refresh serves %q, want the rotated access-2", post.Secret)
	}
}

// TestRefreshRotatesRefreshToken pins test 4: a refresh that returns a rotated
// access token AND a new refresh token updates the cache (peek), so a SUBSEQUENT
// refresh exchanges the rotated refresh token, not the original.
func TestRefreshRotatesRefreshToken(t *testing.T) {
	token := func(_ context.Context, i int) (domain.Credential, error) {
		return domain.Credential{Subject: "u", Secret: "access-1", Refresh: "refresh-1", ExpiresIn: time.Minute}, nil
	}
	p, _ := NewLoginProvider(token)

	var seenRefresh []string
	var gen int64
	p.SetRefreshToken(func(_ context.Context, _ int, cur domain.Credential) (domain.Credential, error) {
		seenRefresh = append(seenRefresh, cur.Refresh)
		n := atomic.AddInt64(&gen, 1)
		return domain.Credential{
			Subject:   cur.Subject,
			Secret:    "access-rot-" + itoaTest(int(n)),
			Refresh:   "refresh-rot-" + itoaTest(int(n)),
			ExpiresIn: 2 * time.Minute,
		}, nil
	})

	if _, err := p.Acquire(context.Background(), 0); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// First refresh exchanges the original refresh-1.
	c1, err := p.Refresh(context.Background(), 0)
	if err != nil {
		t.Fatalf("refresh 1: %v", err)
	}
	if c1.Refresh != "refresh-rot-1" {
		t.Errorf("first refresh rotated refresh token = %q, want refresh-rot-1", c1.Refresh)
	}
	if c1.ExpiresIn != 2*time.Minute {
		t.Errorf("first refresh expiry = %s, want 2m", c1.ExpiresIn)
	}
	// Second refresh must exchange the ROTATED refresh token from the cache.
	c2, err := p.Refresh(context.Background(), 0)
	if err != nil {
		t.Fatalf("refresh 2: %v", err)
	}
	if c2.Refresh != "refresh-rot-2" {
		t.Errorf("second refresh = %q, want refresh-rot-2", c2.Refresh)
	}
	want := []string{"refresh-1", "refresh-rot-1"}
	if len(seenRefresh) != 2 || seenRefresh[0] != want[0] || seenRefresh[1] != want[1] {
		t.Errorf("refresh tokens presented to the exchange = %v, want %v (rotation read from cache)", seenRefresh, want)
	}
}

// TestRefreshErrorSurfaces pins that a real refresh-token exchange FAILURE surfaces
// as an error (so the runtime tags ErrorClassAuthRefresh) rather than silently
// falling back to re-login after a real attempt.
func TestRefreshErrorSurfaces(t *testing.T) {
	var tokenCalls int64
	token := func(_ context.Context, i int) (domain.Credential, error) {
		atomic.AddInt64(&tokenCalls, 1)
		return domain.Credential{Secret: "access-1", Refresh: "refresh-1"}, nil
	}
	p, _ := NewLoginProvider(token)
	p.SetRefreshToken(func(_ context.Context, _ int, _ domain.Credential) (domain.Credential, error) {
		return domain.Credential{}, errors.New("refresh endpoint 400")
	})
	if _, err := p.Acquire(context.Background(), 0); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	atomic.StoreInt64(&tokenCalls, 0)

	if _, err := p.Refresh(context.Background(), 0); err == nil {
		t.Fatal("a real refresh-token exchange failure must surface, not silently re-login")
	}
	if got := atomic.LoadInt64(&tokenCalls); got != 0 {
		t.Errorf("login (token) path called %d times after a refresh failure, want 0 (no silent fallback)", got)
	}
}

// TestRefreshSingleFlightDedupesConcurrent pins the M2 baseline: N concurrent
// Refresh(key) calls for the SAME key collapse to EXACTLY ONE exchange whose result
// every caller receives. Without single-flight every goroutine would fire its own
// grant_type=refresh_token POST against the now-stale refresh token (lost-update /
// self-DDoS in the shared client_credentials scope, where every session collapses to
// cache key 0). Run with -race: the exchange runs WITHOUT holding l.mu, only the map
// bookkeeping is locked.
func TestRefreshSingleFlightDedupesConcurrent(t *testing.T) {
	var exchanges int64
	gate := make(chan struct{}) // released once all callers are in-flight
	token := func(_ context.Context, _ int) (domain.Credential, error) {
		// Seed a credential that carries a refresh token so Refresh takes the exchange
		// path (not the re-login fallback).
		return domain.Credential{Subject: "u", Secret: "access-0", Refresh: "refresh-0"}, nil
	}
	p, _ := NewLoginProvider(token)
	p.SetRefreshToken(func(_ context.Context, _ int, cur domain.Credential) (domain.Credential, error) {
		atomic.AddInt64(&exchanges, 1)
		<-gate // hold the single exchange until the test confirms everyone is waiting
		return domain.Credential{Subject: cur.Subject, Secret: "access-rotated", Refresh: "refresh-rotated"}, nil
	})

	// Seed the cache so peek() finds a credential with a refresh token.
	if _, err := p.Acquire(context.Background(), 0); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	const n = 50
	creds := make([]domain.Credential, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			creds[idx], errs[idx] = p.Refresh(context.Background(), 0) // all for the SAME key
		}(i)
	}
	// Give every goroutine time to collide on the one in-flight exchange, then release.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := atomic.LoadInt64(&exchanges); got != 1 {
		t.Errorf("%d concurrent Refresh calls fired %d exchanges, want exactly 1 (single-flight)", n, got)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("caller %d got error %v, want nil", i, errs[i])
		}
		if creds[i].Secret != "access-rotated" || creds[i].Refresh != "refresh-rotated" {
			t.Errorf("caller %d got cred %q/%q, want the single rotated access-rotated/refresh-rotated",
				i, creds[i].Secret, creds[i].Refresh)
		}
	}
	// The single rotated credential is in the cache.
	post, _ := p.Acquire(context.Background(), 0)
	if post.Secret != "access-rotated" {
		t.Errorf("cache after concurrent refresh serves %q, want access-rotated", post.Secret)
	}
}

// TestRefreshSequentialAfterInflightStartsNewExchange pins that single-flight only
// collapses CONCURRENT calls: once an in-flight refresh completes, a LATER Refresh
// starts a NEW exchange (sequential 401s each genuinely re-expire the token).
func TestRefreshSequentialAfterInflightStartsNewExchange(t *testing.T) {
	var exchanges int64
	token := func(_ context.Context, _ int) (domain.Credential, error) {
		return domain.Credential{Subject: "u", Secret: "access-0", Refresh: "refresh-0"}, nil
	}
	p, _ := NewLoginProvider(token)
	p.SetRefreshToken(func(_ context.Context, _ int, cur domain.Credential) (domain.Credential, error) {
		n := atomic.AddInt64(&exchanges, 1)
		return domain.Credential{Subject: cur.Subject, Secret: "access-" + itoaTest(int(n)), Refresh: "refresh-" + itoaTest(int(n))}, nil
	})
	if _, err := p.Acquire(context.Background(), 0); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	c1, err := p.Refresh(context.Background(), 0)
	if err != nil {
		t.Fatalf("refresh 1: %v", err)
	}
	c2, err := p.Refresh(context.Background(), 0)
	if err != nil {
		t.Fatalf("refresh 2: %v", err)
	}
	if got := atomic.LoadInt64(&exchanges); got != 2 {
		t.Errorf("two sequential refreshes fired %d exchanges, want 2 (no dedup across non-concurrent calls)", got)
	}
	if c1.Secret == c2.Secret {
		t.Errorf("sequential refreshes returned the same token %q; the second must be a fresh exchange", c1.Secret)
	}
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
