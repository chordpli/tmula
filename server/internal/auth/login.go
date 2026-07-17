package auth

import (
	"context"
	"fmt"
	"sync"

	"github.com/chordpli/tmula/server/internal/domain"
)

// refreshCall tracks one in-flight refresh-token exchange so concurrent Refresh
// calls for the same key collapse to a single exchange instead of each firing its
// own grant_type=refresh_token POST against the now-stale refresh token. It mirrors
// signupCall but lives in a SEPARATE map: an Acquire and a Refresh for the same key
// are independent operations (Acquire serves the cache, Refresh always rotates), so
// they must not share an in-flight slot.
type refreshCall struct {
	done chan struct{}
	cred domain.Credential
	err  error
}

// TokenFunc mints one principal's token by running a login flow and returns its
// credential. It is injected so the login provider is independent of any concrete
// login transport (the transport lives a layer above, in the api package, and is
// compiled there). userIndex keys the principal so each virtual user logs in as a
// distinct account.
type TokenFunc func(ctx context.Context, userIndex int) (domain.Credential, error)

// RefreshTokenFunc rotates one principal's credential via a real OAuth2
// grant_type=refresh_token exchange. It takes the CURRENT credential so it can read
// cur.Refresh (the captured refresh token) and returns the rotated credential (a new
// access token, the rotated-or-carried-forward refresh token, and the new lifetime).
// It is injected so the login provider is independent of any concrete refresh
// transport (the transport lives a layer above, in the api package, and is compiled
// from the login flow's token POST). It is OPTIONAL: when nil — or when the current
// credential carries no refresh token — the login provider falls back to re-running
// the full login (TokenFunc). userIndex keys the principal exactly as TokenFunc does,
// so a refresh rotates the same account the login minted.
type RefreshTokenFunc func(ctx context.Context, userIndex int, cur domain.Credential) (domain.Credential, error)

// LoginProvider mints a token per virtual user by running a login flow up front,
// caching the result so each user keeps a stable identity for the run. It is a
// near-clone of BootstrapSignupProvider: cache-by-index, in-flight dedup so
// concurrent Acquire calls for the same user share one login, and a failed mint is
// not cached so it can be retried. It composes ABOVE the Provider seam — the
// static PoolProvider path is untouched.
type LoginProvider struct {
	token TokenFunc
	// refreshToken, when set, rotates a credential via a real grant_type=refresh_token
	// exchange instead of re-running the login. It is set once at build time
	// (SetRefreshToken), before Refresh is ever called, so it needs no lock of its own.
	// A nil refreshToken keeps Refresh on the re-login (token) path — the safe fallback
	// for a login flow that cannot be expressed as a refresh-token grant.
	refreshToken RefreshTokenFunc
	mu           sync.Mutex
	cache        map[int]domain.Credential
	inflight     map[int]*signupCall
	// refreshInflight dedups concurrent Refresh calls per key (see refreshCall). It is
	// guarded by the same mu as cache/inflight — only the map bookkeeping is locked; the
	// exchange itself runs unlocked, exactly like Acquire's signup.
	refreshInflight map[int]*refreshCall
}

// NewLoginProvider builds a provider that mints tokens on demand via token.
func NewLoginProvider(token TokenFunc) (*LoginProvider, error) {
	if token == nil {
		return nil, fmt.Errorf("auth: login provider needs a token function")
	}
	return &LoginProvider{
		token:           token,
		cache:           make(map[int]domain.Credential),
		inflight:        make(map[int]*signupCall),
		refreshInflight: make(map[int]*refreshCall),
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

// SetRefreshToken installs the refresh-token transport Refresh rotates credentials
// through, mirroring SetTeardown on the bootstrap provider. It is set once at build
// time (the orchestrator wires it from the login flow's derived refresh template),
// before Refresh is ever called, so it needs no lock. A nil func keeps Refresh on the
// re-login (token) path — the safe fallback when the login flow cannot be expressed
// as a grant_type=refresh_token exchange.
func (l *LoginProvider) SetRefreshToken(refresh RefreshTokenFunc) {
	l.refreshToken = refresh
}

// Set replaces the cached credential for userIndex. It is how a mid-run refresh
// records the freshly minted token so a later Acquire serves the fresh one.
func (l *LoginProvider) Set(userIndex int, cred domain.Credential) {
	l.mu.Lock()
	l.cache[userIndex] = cred
	l.mu.Unlock()
}

// peek reads the cached credential for userIndex under the lock. ok is false when no
// credential has been minted for the index yet. It is the read-current step of a
// refresh-token exchange (Refresh reads the current credential so the exchange can
// present cur.Refresh) and is kept a discrete, locked accessor so a later
// single-flight wrapper can compose the three Refresh steps without touching the api
// layer.
func (l *LoginProvider) peek(userIndex int) (domain.Credential, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.cache[userIndex]
	return c, ok
}

// Refresh rotates the cached credential for userIndex and returns the fresh one.
// Unlike Acquire it does NOT serve a cached value — it always rotates — which is the
// mid-run 401 recovery path (reactive re-acquire of the same principal).
//
// Concurrent Refresh calls for the SAME key are SINGLE-FLIGHTED: while one rotation
// is in flight, other callers WAIT on it and return its result (cred+err) rather than
// each firing its own exchange against the now-stale refresh token. This is the same
// in-flight-dedup pattern Acquire uses, on a SEPARATE map (refreshInflight): it bounds
// the shared-scope storm where every session collapses to cache key 0 and would
// otherwise all rotate at once (lost-update / self-DDoS). It collapses only CONCURRENT
// calls — once the in-flight refresh completes a LATER Refresh starts a NEW exchange
// (each genuine re-expiry rotates again). Only the map bookkeeping is locked; the
// exchange runs WITHOUT holding l.mu (it is a network call), exactly like Acquire's
// signup, so -race stays clean.
//
// The actual rotation (refreshOnce) is structured as THREE DISCRETE STEPS so the
// dedup wraps them without touching the api layer: (1) read the current credential,
// (2) exchange it for a fresh one, (3) write the result back. See refreshOnce.
func (l *LoginProvider) Refresh(ctx context.Context, userIndex int) (domain.Credential, error) {
	// TODO(slice3): post-failure backoff. A per-key cooldown (inject a clock; on a
	// recent FAILED attempt return a cooldown error to skip the exchange) was considered
	// here and DEFERRED: single-flight above already bounds the concurrent storm, and a
	// cooldown would convert a legitimate sequential recovery attempt into a forced
	// excused-401 for the cooldown window — degrading the reactive single-retry recovery
	// the runtime depends on (runtime.go classifies any Refresh error as auth-refresh
	// with no retry). Backoff is a secondary refinement; revisit only if a sustained-401
	// sequential storm proves to hammer the token endpoint in practice.
	l.mu.Lock()
	if call, ok := l.refreshInflight[userIndex]; ok {
		// A rotation for this key is already in flight: wait for it and share its result
		// instead of exchanging the (now-stale) refresh token a second time.
		l.mu.Unlock()
		<-call.done
		return call.cred, call.err
	}
	call := &refreshCall{done: make(chan struct{})}
	l.refreshInflight[userIndex] = call
	l.mu.Unlock()

	// The exchange runs UNLOCKED (it is a network call); only the bookkeeping above and
	// below holds l.mu.
	cred, err := l.refreshOnce(ctx, userIndex)

	l.mu.Lock()
	call.cred, call.err = cred, err
	// Remove the in-flight entry so a LATER Refresh (a genuine subsequent re-expiry)
	// starts a fresh exchange rather than re-serving this completed one.
	delete(l.refreshInflight, userIndex)
	l.mu.Unlock()
	close(call.done)
	return cred, err
}

// refreshOnce performs ONE rotation of the cached credential for userIndex, in three
// discrete steps. It carries no dedup of its own — Refresh wraps it in single-flight.
//
// When a refresh-token transport is wired AND the current credential carries a
// refresh token, it performs a real grant_type=refresh_token exchange: read-current
// (peek) → exchange (refreshToken) → write-back (Set). A refresh-token exchange
// FAILURE surfaces as an error (no silent fall back to re-login after a real
// attempt), so the runtime tags it ErrorClassAuthRefresh rather than masking a broken
// refresh endpoint.
//
// Otherwise — no refresh transport, or no captured refresh token — it FALLS BACK to
// re-running the full login (token), exactly as before this seam existed. A failed
// refresh leaves the existing cache untouched and is returned to the caller.
func (l *LoginProvider) refreshOnce(ctx context.Context, userIndex int) (domain.Credential, error) {
	if l.refreshToken != nil {
		// Step 1 (read-current): the exchange needs the current refresh token.
		if cur, ok := l.peek(userIndex); ok && cur.Refresh != "" {
			// Step 2 (exchange): a real grant_type=refresh_token POST. A failure surfaces.
			cred, err := l.refreshToken(ctx, userIndex, cur)
			if err != nil {
				return domain.Credential{}, fmt.Errorf("auth: refresh-token exchange user %d: %w", userIndex, err)
			}
			// Step 3 (write-back): record the rotated credential so a later Acquire and the
			// next Refresh both see it (the rotated refresh token is read on the next cycle).
			l.Set(userIndex, cred)
			return cred, nil
		}
	}
	// Fallback: re-run the login flow (no refresh transport, or no captured refresh
	// token to exchange).
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
