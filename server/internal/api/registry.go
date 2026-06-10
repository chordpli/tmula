package api

import (
	"fmt"

	"github.com/chordpli/tmula/server/internal/domain"
)

func (s *Server) nextID(prefix string) domain.ID {
	return domain.ID(fmt.Sprintf("%s-%d", prefix, s.seq.Add(1)))
}

// registerRunLocked records a run in the registry and its insertion order. The
// caller must hold s.mu.
func (s *Server) registerRunLocked(id domain.ID, rs *runState) {
	s.runs[id] = rs
	s.runOrder = append(s.runOrder, id)
}

// enforceRunCapLocked evicts the oldest TERMINAL runs (and their specs) until the
// retained-run count is at or below cap. A running or pending run is skipped and
// never evicted, so when the oldest runs are all still in flight the set may stay
// above cap until they finish. The caller must hold s.mu.
func (s *Server) enforceRunCapLocked(cap int) {
	if cap <= 0 || len(s.runs) <= cap {
		return
	}
	kept := s.runOrder[:0:0] // fresh backing array; we rewrite the order slice
	for _, id := range s.runOrder {
		rs, ok := s.runs[id]
		if !ok {
			continue // already gone: drop the stale order entry
		}
		// Evict the oldest terminal runs first, but only while still over cap.
		// len(s.runs) shrinks with each delete, so the guard tracks the live count.
		if len(s.runs) > cap && runStateTerminal(rs) {
			delete(s.runs, id)
			delete(s.specs, id)
			continue
		}
		kept = append(kept, id)
	}
	s.runOrder = kept
}
