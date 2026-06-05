// Package safety guards every outbound request: a target host allowlist
// (dev/staging by default, prod locked), a hard rate + concurrency cap, and a
// kill switch (always-on manual stop plus an opt-in, conservative automatic
// trip). Because the tool deliberately concentrates traffic, a misfire would be
// a self-inflicted outage; these guards make that hard to do by accident.
package safety

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

// KilledError is returned once the kill switch has tripped.
type KilledError struct{ Reason string }

func (e *KilledError) Error() string { return "safety: killed: " + e.Reason }

// LimitError is returned when the rate or concurrency cap is hit.
type LimitError struct{ Kind string }

func (e *LimitError) Error() string { return "safety: limit exceeded: " + e.Kind }

// AutoKill configures the opt-in automatic kill switch. It is disabled by
// default (a nil *AutoKill) so that saturation can actually be observed; when
// enabled it should stay conservative.
type AutoKill struct {
	ErrorRateThreshold float64 // trip when rolling error rate exceeds this (0..1)
	MinSamples         int     // never trip before this many outcomes
}

// Config parameterizes a Guard.
type Config struct {
	Allowlist      []string  // host patterns: exact or leading "*." wildcard
	MaxRPS         int       // token-bucket rate ceiling
	MaxConcurrency int       // in-flight ceiling
	AutoKill       *AutoKill // nil = automatic kill disabled
}

// Guard enforces the safety policy. It is safe for concurrent use.
type Guard struct {
	mu sync.Mutex

	allow    []string
	maxRPS   float64
	maxConc  int
	autoKill *AutoKill

	killed bool
	reason string

	tokens   float64
	lastFill time.Time
	inFlight int

	total int
	errs  int

	now func() time.Time
}

// NewGuard builds a Guard from an explicit config.
func NewGuard(cfg Config) (*Guard, error) {
	if len(cfg.Allowlist) == 0 {
		return nil, fmt.Errorf("safety: allowlist must not be empty")
	}
	if cfg.MaxRPS <= 0 || cfg.MaxConcurrency <= 0 {
		return nil, fmt.Errorf("safety: MaxRPS and MaxConcurrency must be > 0")
	}
	now := time.Now
	return &Guard{
		allow:    cfg.Allowlist,
		maxRPS:   float64(cfg.MaxRPS),
		maxConc:  cfg.MaxConcurrency,
		autoKill: cfg.AutoKill,
		tokens:   float64(cfg.MaxRPS),
		lastFill: now(),
		now:      now,
	}, nil
}

// NewGuardForEnv builds a Guard for a target environment. A prod-locked
// environment is refused unless allowProd is explicitly true (policy §1).
func NewGuardForEnv(env domain.TargetEnv, autoKill *AutoKill, allowProd bool) (*Guard, error) {
	if err := env.Validate(); err != nil {
		return nil, err
	}
	if env.EnvClass == domain.EnvProdLocked && !allowProd {
		return nil, fmt.Errorf("safety: target env is prod-locked; explicit unlock required (policy §1)")
	}
	return NewGuard(Config{
		Allowlist:      env.Allowlist,
		MaxRPS:         env.RateCap.MaxRPS,
		MaxConcurrency: env.RateCap.MaxConcurrency,
		AutoKill:       autoKill,
	})
}

// setClock overrides the time source (tests only).
func (g *Guard) setClock(f func() time.Time) {
	g.mu.Lock()
	g.now = f
	g.lastFill = f()
	g.mu.Unlock()
}

// AllowHost reports whether a request to target is permitted by the allowlist
// (and that the guard has not been killed).
func (g *Guard) AllowHost(target string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.killed {
		return &KilledError{g.reason}
	}
	host := parseHost(target)
	if host == "" {
		return fmt.Errorf("safety: cannot parse host from %q", target)
	}
	if !matchAny(host, g.allow) {
		return fmt.Errorf("safety: host %q not in allowlist", host)
	}
	return nil
}

// Reserve takes one rate token and one concurrency slot. The caller must call
// Release when the request completes. It returns a *KilledError or *LimitError
// when not permitted.
func (g *Guard) Reserve() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.killed {
		return &KilledError{g.reason}
	}
	g.refillLocked()
	if g.inFlight >= g.maxConc {
		return &LimitError{Kind: "concurrency"}
	}
	if g.tokens < 1 {
		return &LimitError{Kind: "rps"}
	}
	g.tokens--
	g.inFlight++
	return nil
}

// Release returns a concurrency slot taken by Reserve.
func (g *Guard) Release() {
	g.mu.Lock()
	if g.inFlight > 0 {
		g.inFlight--
	}
	g.mu.Unlock()
}

func (g *Guard) refillLocked() {
	now := g.now()
	elapsed := now.Sub(g.lastFill).Seconds()
	if elapsed <= 0 {
		return
	}
	g.tokens += elapsed * g.maxRPS
	if g.tokens > g.maxRPS {
		g.tokens = g.maxRPS // burst capped at one second of rate
	}
	g.lastFill = now
}

// Kill trips the manual kill switch. Subsequent reservations are refused.
func (g *Guard) Kill(reason string) {
	g.mu.Lock()
	if !g.killed {
		g.killed = true
		g.reason = reason
	}
	g.mu.Unlock()
}

// Killed reports whether the kill switch has tripped and why.
func (g *Guard) Killed() (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.killed, g.reason
}

// ReportOutcome feeds a request result to the auto-kill policy. ok=false counts
// as an error. When the rolling error rate exceeds the configured threshold
// (after MinSamples) the guard trips automatically.
func (g *Guard) ReportOutcome(ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.total++
	if !ok {
		g.errs++
	}
	if g.autoKill == nil || g.killed || g.total < g.autoKill.MinSamples {
		return
	}
	rate := float64(g.errs) / float64(g.total)
	if rate > g.autoKill.ErrorRateThreshold {
		g.killed = true
		g.reason = fmt.Sprintf("auto: error rate %.2f exceeded threshold %.2f over %d samples", rate, g.autoKill.ErrorRateThreshold, g.total)
	}
}

func parseHost(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func matchAny(host string, patterns []string) bool {
	host = strings.ToLower(host)
	for _, p := range patterns {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == host {
			return true
		}
		if strings.HasPrefix(p, "*.") && strings.HasSuffix(host, p[1:]) {
			return true
		}
	}
	return false
}
