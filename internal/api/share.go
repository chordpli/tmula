package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

// shareEntry maps an opaque token to a run, with an optional expiry.
type shareEntry struct {
	runID     domain.ID
	expiresAt *time.Time
}

// shareRegistry owns the share-token bookkeeping (the token->entry map and the
// insertion-order slice) behind its own mutex, decoupling it from the Server's
// coarse run-state lock. Share access never shares a critical section with run
// state, so a dedicated mutex preserves behavior while narrowing the coupling.
type shareRegistry struct {
	mu    sync.Mutex
	m     map[string]shareEntry
	order []string
	// now reads the live Server clock at call time (not a construction-time
	// snapshot), so a test that reassigns s.now to drive expiry is honored.
	now func() time.Time
}

// newShareRegistry builds an empty registry whose expiry checks read the given
// clock. Pass a closure over the live Server clock (e.g. func() time.Time {
// return s.now() }) so a later s.now reassignment is reflected here.
func newShareRegistry(now func() time.Time) *shareRegistry {
	return &shareRegistry{
		m:   make(map[string]shareEntry),
		now: now,
	}
}

// add records a share token and then bounds the registry to cap — both under a
// single lock, exactly as createShare did (register + enforceCap atomically), so
// no eviction can interleave between the insert and the cap check.
func (r *shareRegistry) add(token string, entry shareEntry, cap int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registerLocked(token, entry)
	r.enforceCapLocked(cap)
}

// getAndExpire looks up a token and, if it is found but expired, drops it — all
// under one lock, exactly as getSharedReport did (lookup + expire-on-read
// atomically). It returns the entry, whether the token was present, and whether
// it was expired (and therefore deleted). Expiry is judged against r.now().
func (r *shareRegistry) getAndExpire(token string) (entry shareEntry, ok bool, expired bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok = r.m[token]
	if ok && entry.expiresAt != nil && r.now().After(*entry.expiresAt) {
		// Drop the expired token on read so a one-shot link cannot linger in the
		// map forever (this is also a small, steady source of share reclamation).
		r.deleteLocked(token)
		expired = true
	}
	return entry, ok, expired
}

// enforceCap bounds the registry to cap under the lock. Exposed for callers
// (and tests) that register entries and then enforce a cap as a separate step.
func (r *shareRegistry) enforceCap(cap int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enforceCapLocked(cap)
}

// has reports whether a token is present (without mutating expiry).
func (r *shareRegistry) has(token string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.m[token]
	return ok
}

// len returns the number of retained tokens.
func (r *shareRegistry) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.m)
}

// registerLocked records a share token and its insertion order. The caller must
// hold r.mu.
func (r *shareRegistry) registerLocked(token string, entry shareEntry) {
	r.m[token] = entry
	r.order = append(r.order, token)
}

// deleteLocked removes a share token from the registry and its order slice. The
// caller must hold r.mu.
func (r *shareRegistry) deleteLocked(token string) {
	if _, ok := r.m[token]; !ok {
		return
	}
	delete(r.m, token)
	kept := r.order[:0:0]
	for _, t := range r.order {
		if t != token {
			kept = append(kept, t)
		}
	}
	r.order = kept
}

// enforceCapLocked bounds the share registry: while over cap it evicts the
// oldest tokens, preferring already-expired ones. Unlike runs a share has no
// in-flight state, so it can always be reclaimed. The caller must hold r.mu.
func (r *shareRegistry) enforceCapLocked(cap int) {
	if cap <= 0 || len(r.m) <= cap {
		return
	}
	now := r.now()
	// First pass: drop expired tokens (cheap, and the most stale).
	for _, token := range append([]string(nil), r.order...) {
		if len(r.m) <= cap {
			break
		}
		if e, ok := r.m[token]; ok && e.expiresAt != nil && now.After(*e.expiresAt) {
			r.deleteLocked(token)
		}
	}
	// Second pass: still over cap -> evict oldest-first regardless of expiry.
	kept := r.order[:0:0]
	for _, token := range r.order {
		if _, ok := r.m[token]; !ok {
			continue
		}
		if len(r.m) > cap {
			delete(r.m, token)
			continue
		}
		kept = append(kept, token)
	}
	r.order = kept
}

// publicKillReason replaces a run's internal KillReason on the shared (PII-masked)
// report path. A failed run's KillReason is the raw error text, which can carry
// control-plane internals (e.g. a worker address like dial worker "10.0.0.5:7000").
// A share-token viewer is untrusted, so they see only this generic string; the
// operator report keeps the full detail.
const publicKillReason = "run did not complete"

// createShare issues an opaque, read-only share token for a run's report. An
// optional ?ttl=<seconds> sets an expiry. This is the operator action behind
// AD-013: viewers get a token, nothing else.
func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("id"))
	// A run is shareable if it is live in the cache or persisted in the store, so
	// an operator can still share a report whose live state was evicted.
	if _, ok := s.reportFor(id); !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("run %q not found", id))
		return
	}

	entry := shareEntry{runID: id}
	if ttl := r.URL.Query().Get("ttl"); ttl != "" {
		secs, err := strconv.Atoi(ttl)
		if err != nil || secs <= 0 {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid ttl %q", ttl))
			return
		}
		exp := s.now().Add(time.Duration(secs) * time.Second)
		entry.expiresAt = &exp
	}

	token, err := newToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.shareReg.add(token, entry, maxRetainedShares)

	writeJSON(w, http.StatusCreated, map[string]string{
		"token": token,
		"url":   "/reports/shared/" + token,
		"scope": string(domain.RoleViewer),
	})
}

// getSharedReport serves a read-only, PII-masked report for a share token. No
// run controls are exposed — viewers can read, nothing more.
func (s *Server) getSharedReport(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	entry, ok, expired := s.shareReg.getAndExpire(token)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("share not found"))
		return
	}
	if expired {
		writeErr(w, http.StatusGone, fmt.Errorf("share expired"))
		return
	}
	// The run is served live from the cache or rebuilt from the store, so a shared
	// link keeps working after the live run state is evicted or a restart drops it.
	rep, found := s.reportFor(entry.runID)
	if !found {
		writeErr(w, http.StatusNotFound, fmt.Errorf("share not found"))
		return
	}

	// reportFor returns a fresh Report whose Run is a value copy, so scrubbing the
	// KillReason here cannot affect the operator report. The field-name masker does
	// not redact killReason, and its raw text can expose control-plane internals
	// (worker addresses, internal errors), so replace it with a generic public
	// string before masking and serving to the untrusted share-token viewer.
	if rep.Run.KillReason != "" {
		rep.Run.KillReason = publicKillReason
	}

	data, err := json.Marshal(rep)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	masked := s.masker.MaskJSON(data)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(masked)
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("api: generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
