package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

// shareEntry maps an opaque token to a run, with an optional expiry.
type shareEntry struct {
	runID     domain.ID
	expiresAt *time.Time
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
	s.mu.Lock()
	_, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
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
	s.mu.Lock()
	s.registerShareLocked(token, entry)
	s.enforceShareCapLocked(maxRetainedShares)
	s.mu.Unlock()

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
	expired := false
	s.mu.Lock()
	entry, ok := s.shares[token]
	rs := s.runs[entry.runID]
	if ok && entry.expiresAt != nil && s.now().After(*entry.expiresAt) {
		// Drop the expired token on read so a one-shot link cannot linger in the
		// map forever (this is also a small, steady source of share reclamation).
		s.deleteShareLocked(token)
		expired = true
	}
	s.mu.Unlock()
	if !ok || rs == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("share not found"))
		return
	}
	if expired {
		writeErr(w, http.StatusGone, fmt.Errorf("share expired"))
		return
	}

	// report() returns a fresh Report whose Run is a value copy, so scrubbing the
	// KillReason here cannot affect the operator report. The field-name masker does
	// not redact killReason, and its raw text can expose control-plane internals
	// (worker addresses, internal errors), so replace it with a generic public
	// string before masking and serving to the untrusted share-token viewer.
	rep := rs.report()
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

// registerShareLocked records a share token and its insertion order. The caller
// must hold s.mu.
func (s *Server) registerShareLocked(token string, entry shareEntry) {
	s.shares[token] = entry
	s.shareOrder = append(s.shareOrder, token)
}

// deleteShareLocked removes a share token from the registry and its order slice.
// The caller must hold s.mu.
func (s *Server) deleteShareLocked(token string) {
	if _, ok := s.shares[token]; !ok {
		return
	}
	delete(s.shares, token)
	kept := s.shareOrder[:0:0]
	for _, t := range s.shareOrder {
		if t != token {
			kept = append(kept, t)
		}
	}
	s.shareOrder = kept
}

// enforceShareCapLocked bounds the share registry: while over cap it evicts the
// oldest tokens, preferring already-expired ones. Unlike runs a share has no
// in-flight state, so it can always be reclaimed. The caller must hold s.mu.
func (s *Server) enforceShareCapLocked(cap int) {
	if cap <= 0 || len(s.shares) <= cap {
		return
	}
	now := s.now()
	// First pass: drop expired tokens (cheap, and the most stale).
	for _, token := range append([]string(nil), s.shareOrder...) {
		if len(s.shares) <= cap {
			break
		}
		if e, ok := s.shares[token]; ok && e.expiresAt != nil && now.After(*e.expiresAt) {
			s.deleteShareLocked(token)
		}
	}
	// Second pass: still over cap -> evict oldest-first regardless of expiry.
	kept := s.shareOrder[:0:0]
	for _, token := range s.shareOrder {
		if _, ok := s.shares[token]; !ok {
			continue
		}
		if len(s.shares) > cap {
			delete(s.shares, token)
			continue
		}
		kept = append(kept, token)
	}
	s.shareOrder = kept
}

func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("api: generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
