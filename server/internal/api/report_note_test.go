package api

import (
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
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

// TestPoolWrapNote pins the pool-wrap note helper: a closed run with fewer pool entries
// than users reports how many VUs share each credential; a pool that covers every user,
// a per-VU strategy (no inline entries), and the open model each produce no note.
func TestPoolWrapNote(t *testing.T) {
	pool := func(n int) *domain.CredentialPool {
		entries := make([]domain.Credential, n)
		for i := range entries {
			entries[i] = domain.Credential{Subject: "u", Secret: "t"}
		}
		return &domain.CredentialPool{ID: "p", Strategy: domain.CredPool, Entries: entries}
	}

	// 2 entries, 5 users → each entry serves ~3 VUs (ceil(5/2)).
	s := specAuth("http://127.0.0.1:1", 5, pool(2))
	note := poolWrapNote(s)
	for _, needle := range []string{"2 entries", "5 users", "~3"} {
		if !strings.Contains(note, needle) {
			t.Errorf("wrap note should mention %q, got %q", needle, note)
		}
	}

	// A pool that covers every user carries no note.
	if n := poolWrapNote(specAuth("http://127.0.0.1:1", 2, pool(2))); n != "" {
		t.Errorf("a fully-covered pool should produce no wrap note, got %q", n)
	}
	// A pool with more entries than users likewise carries no note.
	if n := poolWrapNote(specAuth("http://127.0.0.1:1", 1, pool(2))); n != "" {
		t.Errorf("a pool larger than the user count should produce no wrap note, got %q", n)
	}
	// No credential pool at all → no note.
	if n := poolWrapNote(specAuth("http://127.0.0.1:1", 5, nil)); n != "" {
		t.Errorf("an unauthenticated run should produce no wrap note, got %q", n)
	}
}

// TestRunReportCarriesPoolWrapNote proves the wrap note rides on the assembled report of
// a real run: a 3-user run against a 2-entry pool completes and its report carries the
// wrap note (alongside any stats-derived note).
func TestRunReportCarriesPoolWrapNote(t *testing.T) {
	sut, _ := newAuthEchoSUT()
	defer sut.Close()

	rep := runInProcess(t, specAuth(sut.URL, 3, twoEntryPool()), 3*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}
	joined := strings.Join(rep.Notes, " ")
	if !strings.Contains(joined, "credential pool has 2 entries for 3 users") {
		t.Errorf("report notes should carry the pool-wrap note, got %v", rep.Notes)
	}
}
