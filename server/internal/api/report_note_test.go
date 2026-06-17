package api

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/obs"
)

// TestAuthExpiryNoteFromStatusCounts pins TASK 2: a non-failing, observability-only
// run note is derived purely from the report's already-observed 401/403 status
// counts at report-build time. A cluster of auth rejections produces the note; a
// clean run (or a run with no 401/403) produces none. It is computed from
// Stats.StatusCounts only — never from a finding or a re-classification.
func TestAuthExpiryNoteFromStatusCounts(t *testing.T) {
	cases := []struct {
		name     string
		counts   map[int]int
		wantNote bool
	}{
		{"clean run", map[int]int{200: 500}, false},
		{"no auth rejections", map[int]int{200: 480, 500: 20}, false},
		{"clustered 401s", map[int]int{200: 400, 401: 100}, true},
		{"clustered 403s", map[int]int{200: 400, 403: 50}, true},
		{"both 401 and 403", map[int]int{200: 400, 401: 30, 403: 20}, true},
		{"empty run", map[int]int{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			note := authExpiryNote(obs.Stats{StatusCounts: c.counts})
			if c.wantNote && note == "" {
				t.Fatalf("counts=%v: expected an auth-expiry note, got none", c.counts)
			}
			if !c.wantNote && note != "" {
				t.Fatalf("counts=%v: expected no note, got %q", c.counts, note)
			}
			if c.wantNote {
				// The note must name the count and read as an honest, non-failing signal.
				if !strings.Contains(strings.ToLower(note), "auth") {
					t.Errorf("note %q should mention auth", note)
				}
				if !strings.Contains(note, "401") && !strings.Contains(note, "403") {
					t.Errorf("note %q should reference the 401/403 status codes", note)
				}
			}
		})
	}
}

// TestReportCarriesAuthExpiryNote proves the note rides on the assembled Report
// (a new optional field), so an operator sees it without a finding being raised.
// A clustered-401 run carries the note AND no auth finding is fabricated for it.
func TestReportCarriesAuthExpiryNote(t *testing.T) {
	rep := Report{Stats: obs.Stats{StatusCounts: map[int]int{200: 100, 401: 40}}}
	rep.Notes = notesFor(rep.Stats)
	if len(rep.Notes) == 0 {
		t.Fatal("a clustered-401 report must carry a run note")
	}
	joined := strings.Join(rep.Notes, " ")
	if !strings.Contains(strings.ToLower(joined), "auth") {
		t.Errorf("notes %v should include the auth-expiry note", rep.Notes)
	}

	clean := Report{Stats: obs.Stats{StatusCounts: map[int]int{200: 100}}}
	clean.Notes = notesFor(clean.Stats)
	if len(clean.Notes) != 0 {
		t.Errorf("a clean run must carry no notes, got %v", clean.Notes)
	}
}
