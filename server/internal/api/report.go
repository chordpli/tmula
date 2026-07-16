package api

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/obs"
	"github.com/chordpli/tmula/server/internal/store"
)

// Report is the operator-facing run report. Run.Mode reports the execution
// topology (local or distributed); Workers is the number of remote workers a
// distributed run fanned out to (0 for a local run).
type Report struct {
	Run      domain.RunExecution `json:"run"`
	Stats    obs.Stats           `json:"stats"`
	Findings []domain.Finding    `json:"findings"`
	Workers  int                 `json:"workers"`
	// Notes are non-failing, observability-only run remarks derived purely from the
	// run's aggregated stats at report-build time — never findings and never a
	// re-classification of any observation. The "auth likely expired" note (a
	// cluster of 401/403 responses, the honest tell of a static token pool that
	// expired mid-run) lives here. Optional: omitted when there is nothing to note.
	Notes []string `json:"notes,omitempty"`
	// ServerMetrics carries the Prometheus series fetched over the run's window
	// when the run opted in (RunSpec.Metrics); MetricsError notes a fetch
	// problem. Both are live-report extras: they are not persisted, so a report
	// rebuilt from the store omits them.
	ServerMetrics []domain.MetricSeries `json:"serverMetrics,omitempty"`
	MetricsError  string                `json:"metricsError,omitempty"`
}

// authRejectionCodes are the HTTP statuses that signal an auth rejection (401
// Unauthorized, 403 Forbidden). A cluster of them across a run is the tell that a
// static credential pool expired or was rejected mid-run.
var authRejectionCodes = [...]int{401, 403}

// notesFor builds the report's non-failing observability notes from already
// aggregated stats. It is the single place report() and reportFromStore() derive
// notes, so the live report and one rebuilt from the store agree. It reads only
// the status-count tallies — it classifies nothing and raises no finding.
func notesFor(stats obs.Stats) []string {
	var notes []string
	if n := authExpiryNote(stats); n != "" {
		notes = append(notes, n)
	}
	return notes
}

// authExpiryNote returns the "auth may have expired" run note when the run saw any
// auth-rejection (401/403) responses, or "" otherwise. The threshold is simple and
// deliberate: > 0 auth rejections surfaces the note (any 401/403 is worth telling
// an operator about — it is the honest signal a token pool expired or was rejected
// mid-run, and the note never fails the run, so a low bar costs nothing). It is
// computed purely from the run's observed status counts at report-build time and
// is NOT a finding and NOT a reclassification — the obs predicates are untouched.
func authExpiryNote(stats obs.Stats) string {
	n := 0
	for _, code := range authRejectionCodes {
		n += stats.StatusCounts[code]
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("auth may have expired or been rejected (%d response(s) were 401/403)", n)
}

// openSetupFailureThreshold is the fraction of launched open-model sessions whose auth
// setup may fail before the run additionally raises a finding (not just a note). A few
// skipped sessions are a note-worthy footnote; more than one in ten failing to
// authenticate is a material result the run should flag.
const openSetupFailureThreshold = 0.10

// isSignificantSetupFailure reports whether the auth-setup failure count is large enough
// (past openSetupFailureThreshold of launched) to warrant a finding, not just a note.
func isSignificantSetupFailure(setupErrors, launched int) bool {
	return launched > 0 && float64(setupErrors) > openSetupFailureThreshold*float64(launched)
}

// setupErrorNote renders the open-model auth-setup note: how many of the launched
// sessions were skipped because they could not authenticate, one representative cause,
// and the open=skip vs closed=abort asymmetry so an operator understands why the run
// still completed instead of failing. The first error is a setup cause (a failed
// credential acquire), never a secret.
func setupErrorNote(setupErrors, launched int, firstErr error) string {
	cause := "unknown"
	if firstErr != nil {
		cause = firstErr.Error()
	}
	return fmt.Sprintf("%d of %d sessions failed auth setup and were skipped; first error: %s "+
		"(the open model skips a session it cannot authenticate and keeps going, unlike the closed model, which aborts the whole run on the first auth-setup failure)",
		setupErrors, launched, cause)
}

// authSetupFinding builds the finding raised when a material fraction of open-model
// sessions failed auth setup. It is a threshold-class finding keyed on the stable
// "auth-setup" metric identity (like "error-rate"), so the same issue keys identically
// across runs, at warning severity — the run produced data, but a large share of the
// intended load never authenticated.
func authSetupFinding(runID domain.ID, setupErrors, launched int, now time.Time) domain.Finding {
	pct := 0.0
	if launched > 0 {
		pct = 100 * float64(setupErrors) / float64(launched)
	}
	return domain.Finding{
		RunID:       runID,
		Category:    domain.FindingThreshold,
		Severity:    domain.SeverityWarning,
		EvidenceRef: "auth-setup",
		FirstSeen:   now,
		Count:       setupErrors,
		Description: fmt.Sprintf("%d of %d open-model sessions (%.1f%%) could not authenticate and were skipped; the load actually exercised is smaller than requested — check the credential pool/login flow", setupErrors, launched, pct),
	}
}

// report assembles the report for a run (caller must not hold rs.mu). Workers is
// taken from the run itself (set at creation, persisted on finalize) so the live
// report and one rebuilt from the store agree on the topology.
func (rs *runState) report() Report {
	rs.mu.Lock()
	exec := rs.exec
	findings := append([]domain.Finding(nil), rs.findings...)
	serverMetrics := append([]domain.MetricSeries(nil), rs.serverMetrics...)
	metricsErr := rs.metricsErr
	staticNotes := append([]string(nil), rs.staticNotes...)
	rs.mu.Unlock()
	stats := rs.stats()
	// Static (spec-derived) notes come first — they are run-setup facts (a wrapped
	// credential pool) — then the stats-derived notes (the 401/403 auth-expiry tell).
	notes := append(staticNotes, notesFor(stats)...)
	return Report{
		Run: exec, Stats: stats, Findings: findings, Workers: exec.Workers,
		Notes:         notes,
		ServerMetrics: serverMetrics, MetricsError: metricsErr,
	}
}

// poolWrapNote returns the "credential pool is shared across virtual users" note when a
// closed run has FEWER pool entries than virtual users, so each entry authenticates
// several VUs (the pool wraps around by index). It is a spec-derived setup fact — not a
// finding and not a re-classification — surfaced so an operator knows a 200k-user run is
// only exercising, say, 1000 distinct principals. It returns "" when the pool covers every
// user, when there are no inline entries (a mint/bootstrap/exec run mints per-VU), or for
// the open model (which has no fixed user count to compare against).
func poolWrapNote(spec RunSpec) string {
	if spec.IsOpen() || spec.CredentialPool == nil {
		return ""
	}
	n := len(spec.CredentialPool.Entries)
	if n == 0 {
		return ""
	}
	m := spec.PoolSize()
	if m <= n {
		return ""
	}
	// Ceiling division: the busiest entry serves this many VUs.
	k := (m + n - 1) / n
	return fmt.Sprintf("credential pool has %d entries for %d users; each credential is shared by ~%d virtual users", n, m, k)
}

// startNotesFor collects the spec-derived run notes known before the run starts. It is
// the single place StartRun assembles rs.staticNotes, so a new setup note is added here.
func startNotesFor(spec RunSpec) []string {
	var notes []string
	if n := poolWrapNote(spec); n != "" {
		notes = append(notes, n)
	}
	return notes
}

// reportFor returns a run's report and whether it was found. A live run in the
// in-memory cache is served directly; otherwise the run is rebuilt from the
// store (the system of record), so a report stays available after the live state
// is evicted past the retention bound or lost to a restart. The bool is false
// only when neither the cache nor the store knows the run.
func (s *Server) reportFor(id domain.ID) (Report, bool) {
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if ok {
		return rs.report(), true
	}
	return s.reportFromStore(id)
}

// reportFromStore rebuilds a finalized run's report from persisted run + stats +
// findings. A missing run is the not-found case; stats or findings absent (an
// older snapshot, or a run that never finalized) degrade to zero-values rather
// than failing, since the run row alone still makes a meaningful report.
func (s *Server) reportFromStore(id domain.ID) (Report, bool) {
	if s.store == nil {
		return Report{}, false
	}
	run, err := s.store.GetRun(id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("load run from store failed", "run", id, "err", err)
		}
		return Report{}, false
	}
	stats, err := s.store.Stats(id)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		slog.Warn("load stats from store failed", "run", id, "err", err)
	}
	findings, err := s.store.Findings(id)
	if err != nil {
		slog.Warn("load findings from store failed", "run", id, "err", err)
	}
	return Report{Run: run, Stats: stats, Findings: findings, Workers: run.Workers, Notes: notesFor(stats)}, true
}
