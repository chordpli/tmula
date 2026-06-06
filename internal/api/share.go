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
	s.shares[token] = entry
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
	s.mu.Lock()
	entry, ok := s.shares[token]
	rs := s.runs[entry.runID]
	s.mu.Unlock()
	if !ok || rs == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("share not found"))
		return
	}
	if entry.expiresAt != nil && s.now().After(*entry.expiresAt) {
		writeErr(w, http.StatusGone, fmt.Errorf("share expired"))
		return
	}

	data, err := json.Marshal(rs.report())
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
