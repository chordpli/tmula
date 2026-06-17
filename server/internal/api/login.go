package api

import (
	"context"
	"fmt"
	"sync"

	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/safety"
)

// loginAuth bundles the runtime pieces a CredLogin run is driven by: the login
// provider (cache-by-index + dedup + refresh) and the per-index seed used to make
// each login deterministic. It is built once per run, above the load runner, and
// drives both the initial token mint (Prewarm/Acquire) and the mid-run refresh.
type loginAuth struct {
	provider *auth.LoginProvider
	// shared is true for the client_credentials scope: every user shares one token
	// (cache key 0) and one holder. Per-user mints one token per index.
	shared bool

	// sharedMu guards the lazy construction of the single shared holder. In the
	// shared scope every seed() returns the SAME holder POINTER (and the same
	// refresh closure bound to it), built exactly once here, so one mint serves all
	// sessions and one refresh rotates the token for all of them. It is a pointer,
	// never copied — copying it would give each session an independent token box and
	// silently break the shared (client_credentials) semantics.
	sharedMu      sync.Mutex
	sharedHolder  load.CredentialHolder
	sharedRefresh load.RefreshFunc
}

// loginAuthFor builds the login provider for a CredLogin run by compiling the
// spec's login flow into a token transport and wrapping it in a LoginProvider. It
// returns (nil, nil) for any non-login pool, so callers can branch on it. The
// login runner is guarded by the run's safety policy so the login endpoint obeys
// the same allowlist and rate cap as the simulated traffic. liveRefresh selects
// whether the provider mints live (the run path) — it is always true here; the
// refresh-FREE reproduce variant builds its own provider without wiring a refresh
// onto the user (see reproduce.go).
func (s *Server) loginAuthFor(spec RunSpec, guard *safety.Guard) (*loginAuth, error) {
	if spec.CredentialPool == nil || spec.CredentialPool.Strategy != domain.CredLogin {
		return nil, nil
	}
	if spec.LoginFlow == nil {
		// Validate already rejects this, but guard against a programming error
		// reaching the runtime with no flow to mint from.
		return nil, fmt.Errorf("api: login run has no login flow to mint tokens from")
	}
	flow := LoginFlow{
		Graph:      spec.LoginFlow.Graph,
		Templates:  spec.LoginFlow.Templates,
		Start:      spec.LoginFlow.Start,
		MaxSteps:   spec.LoginFlow.MaxSteps,
		TokenVar:   spec.LoginFlow.TokenVar,
		SubjectVar: spec.LoginFlow.SubjectVar,
		// P8 multi-user login: the pool's Entries are login-INPUT rows (username +
		// password), threaded into the token func so virtual user i logs in as row
		// i%N. They reach BOTH the run path and the reproduce path through this single
		// builder, so reproduce of VU i re-logs-in as the same account deterministically
		// (i%N is a pure function of the index). Empty entries is the single-identity
		// login — unchanged. The CLI resolves a login Source into these Entries at
		// expand time (like the pool strategy), so a login pool only ever arrives here
		// with inline entries, never an unresolved Source.
		Entries: spec.CredentialPool.Entries,
	}
	// A dedicated runner for the login flow: same adapter and base URL as the run,
	// guarded so the login endpoint is allowlist-checked and rate-capped. It carries
	// no result/event sink, so RunOnce (which the transport drives) stays findings-
	// isolated even if those were set.
	runner := load.NewRunner(s.adapter, spec.TargetEnv.BaseURL, flow.Templates, load.WithGuard(guard))
	tokenFunc, err := NewLoginTokenFunc(runner, flow, spec.Seed)
	if err != nil {
		return nil, fmt.Errorf("api: compile login flow: %w", err)
	}
	return newLoginAuthFromToken(tokenFunc, spec.CredentialPool.EffectiveLoginScope() == domain.LoginShared)
}

// newLoginAuthFromToken builds a loginAuth over a token func and scope. It is the
// single construction point for the provider, so the run path and tests build the
// same seam.
func newLoginAuthFromToken(token auth.TokenFunc, shared bool) (*loginAuth, error) {
	provider, err := auth.NewLoginProvider(token)
	if err != nil {
		return nil, fmt.Errorf("api: build login provider: %w", err)
	}
	return &loginAuth{provider: provider, shared: shared}, nil
}

// cacheKey maps a user/arrival index onto the login provider's cache key. Per-user
// keys by the index so each principal mints its own token; shared collapses every
// index onto key 0 so one token (one client_credentials grant) is minted and
// served to all sessions.
func (l *loginAuth) cacheKey(userIndex int) int {
	if l.shared {
		return 0
	}
	return userIndex
}

// seed mints (or serves the cached) credential for userIndex and returns a live
// holder wired with a refresh closure. The holder is the seam runSession reads the
// credential from per step; the refresh re-runs the login for the SAME cache key
// (the same Seed-offset arithmetic the run path keys credentials by, never a new
// index) and rotates the holder in place on a mid-run 401.
//
// For the shared scope every call returns the SAME holder pointer (built once and
// memoized), so a single refresh reaches every session — see sharedHolder. The
// holder is created here, above the runner, exactly once per principal.
func (l *loginAuth) seed(ctx context.Context, userIndex int) (load.CredentialHolder, load.RefreshFunc, error) {
	if l.shared {
		return l.sharedSeed(ctx)
	}
	key := l.cacheKey(userIndex)
	cred, err := l.provider.Acquire(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	holder := load.NewCredentialHolder(cred)
	refresh := l.refreshFunc(key, holder)
	return holder, refresh, nil
}

// sharedSeed returns the single shared holder (and its refresh), building it once
// on first call. Every session that seeds in the shared scope receives the SAME
// holder pointer, so one client_credentials token is minted and one refresh
// rotates it for all of them.
func (l *loginAuth) sharedSeed(ctx context.Context) (load.CredentialHolder, load.RefreshFunc, error) {
	l.sharedMu.Lock()
	defer l.sharedMu.Unlock()
	if l.sharedHolder != nil {
		return l.sharedHolder, l.sharedRefresh, nil
	}
	cred, err := l.provider.Acquire(ctx, 0) // shared cache key is always 0
	if err != nil {
		return nil, nil, err
	}
	holder := load.NewCredentialHolder(cred)
	l.sharedHolder = holder
	l.sharedRefresh = l.refreshFunc(0, holder)
	return l.sharedHolder, l.sharedRefresh, nil
}

// refreshFunc builds the per-principal refresh closure: it re-acquires the token
// for key and rotates holder. It binds key once, so the runtime never re-derives
// an index — the orchestrator owns the index arithmetic.
func (l *loginAuth) refreshFunc(key int, holder load.CredentialHolder) load.RefreshFunc {
	return func(ctx context.Context) error {
		cred, err := l.provider.Refresh(ctx, key)
		if err != nil {
			return err
		}
		holder.Set(cred)
		return nil
	}
}

// Prewarm mints n tokens ahead of the run (per-user) or the single shared token
// (shared), matching how the run will key them — so the first request of every
// session has a token without a synchronous login on the hot path.
func (l *loginAuth) Prewarm(ctx context.Context, n int) error {
	if l.shared {
		_, err := l.provider.Acquire(ctx, 0)
		return err
	}
	return l.provider.Prewarm(ctx, n)
}
