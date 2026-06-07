package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
)

// compileTimeAssertPostgres documents (and forces the compiler to check) that
// *PostgresStore satisfies the Store interface. The package-level assertion in
// postgres.go is the source of truth; this mirrors it in the test binary so the
// guarantee is exercised even when the integration test below is skipped.
var _ Store = (*PostgresStore)(nil)

// TestNewPostgresStoreBadDSN exercises the connection error path without a
// database. An unparseable DSN must surface as a wrapped error, never a nil
// store with nil error.
func TestNewPostgresStoreBadDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s, err := NewPostgresStore(ctx, "postgres://bad host")
	if err == nil {
		t.Fatalf("expected error for bad DSN, got store=%v", s)
	}
	if s != nil {
		t.Errorf("store should be nil on error, got %v", s)
	}
}

// TestPostgresStoreContextLifecycle covers the store's base-context wiring
// without a database. Every Store method runs its pgx call on s.ctx (instead of
// a detached context.Background()), so the context must (a) be live while the
// store is open and (b) be cancelled by Close, which is what lets in-flight
// queries stop promptly on shutdown. The pool is nil here; Close guards it.
func TestPostgresStoreContextLifecycle(t *testing.T) {
	base, cancel := context.WithCancel(context.WithoutCancel(context.Background()))
	s := &PostgresStore{ctx: base, cancel: cancel}

	if err := s.ctx.Err(); err != nil {
		t.Fatalf("store context should be live before Close, got %v", err)
	}
	s.Close()
	if err := s.ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Errorf("store context after Close = %v, want context.Canceled", err)
	}
	// Close must be safe to call twice (idempotent cancel + guarded nil pool).
	s.Close()
}

// TestPostgresStoreContextDetachesDeadline verifies the store's base context is
// independent of the constructor context's deadline: a query context derived
// from a short-lived parent must not already be expired once the store is built,
// so operations after the connect/ping timeout still run.
func TestPostgresStoreContextDetachesDeadline(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer parentCancel()
	// Let the parent's tiny deadline elapse — mirrors a connect/ping ctx expiring
	// after construction.
	<-parent.Done()

	base, cancel := context.WithCancel(context.WithoutCancel(parent))
	s := &PostgresStore{ctx: base, cancel: cancel}
	defer s.Close()

	if err := s.ctx.Err(); err != nil {
		t.Errorf("store context inherited the parent deadline (%v); it should be detached", err)
	}
}

// TestPostgresStoreRoundTrip is an integration test. It is skipped unless
// TMULA_TEST_POSTGRES is set to a usable DSN (e.g.
// postgres://user:pass@localhost:5432/tmula_test?sslmode=disable). When run it
// migrates the schema and round-trips every entity, asserting ErrNotFound for
// missing ids. t.Cleanup truncates the tables it touched so the database is left
// clean for re-runs.
func TestPostgresStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("TMULA_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set TMULA_TEST_POSTGRES to a Postgres DSN to run the integration test")
	}

	ctx := context.Background()
	s, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	t.Cleanup(s.Close)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Migrate must be idempotent: a second call on an existing schema is a no-op.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (second call) must be idempotent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `TRUNCATE experiments, runs, metrics, findings`)
	})

	// --- Experiment round-trip ---
	exp := domain.Experiment{
		ID:              "e-pg-1",
		Name:            "pg-smoke",
		TargetEnvID:     "env-1",
		ScenarioGraphID: "graph-1",
		Params:          domain.ExperimentParams{VirtualUserCount: 5, DeviationRate: 0.25, AuthStrategy: domain.CredPool},
		CreatedAt:       time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := s.SaveExperiment(exp); err != nil {
		t.Fatalf("SaveExperiment: %v", err)
	}
	// Upsert path: saving again with a changed field must replace, not duplicate.
	exp.Name = "pg-smoke-v2"
	if err := s.SaveExperiment(exp); err != nil {
		t.Fatalf("SaveExperiment (update): %v", err)
	}
	gotExp, err := s.GetExperiment(exp.ID)
	if err != nil {
		t.Fatalf("GetExperiment: %v", err)
	}
	if gotExp.Name != "pg-smoke-v2" || gotExp.Params.VirtualUserCount != 5 || !gotExp.CreatedAt.Equal(exp.CreatedAt) {
		t.Errorf("experiment round-trip mismatch: got %+v want %+v", gotExp, exp)
	}
	if _, err := s.GetExperiment("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing experiment should be ErrNotFound, got %v", err)
	}

	// --- Run round-trip ---
	run := domain.RunExecution{
		ID:           "r-pg-1",
		ExperimentID: exp.ID,
		Mode:         domain.RunDistributed,
		Status:       domain.RunRunning,
		StartedAt:    time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := s.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	// Status update via upsert.
	run.Status = domain.RunCompleted
	if err := s.SaveRun(run); err != nil {
		t.Fatalf("SaveRun (update): %v", err)
	}
	gotRun, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if gotRun.Status != domain.RunCompleted || gotRun.ExperimentID != exp.ID {
		t.Errorf("run round-trip mismatch: got %+v want %+v", gotRun, run)
	}
	if _, err := s.GetRun("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing run should be ErrNotFound, got %v", err)
	}

	// --- Metrics round-trip (ordered, append-only) ---
	base := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 3; i++ {
		m := domain.MetricSample{
			RunID:      run.ID,
			TS:         base.Add(time.Duration(i) * time.Millisecond),
			APIID:      "api-1",
			StatusCode: 200 + i,
			LatencyMs:  float64(10 + i),
			WorkerID:   "w-1",
		}
		if err := s.AppendMetric(m); err != nil {
			t.Fatalf("AppendMetric[%d]: %v", i, err)
		}
	}
	metrics, err := s.Metrics(run.ID)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(metrics) != 3 || metrics[0].StatusCode != 200 || metrics[2].StatusCode != 202 {
		t.Fatalf("metrics round-trip = %+v", metrics)
	}
	if metrics[1].LatencyMs != 11 || metrics[1].WorkerID != "w-1" {
		t.Errorf("metric field round-trip mismatch: %+v", metrics[1])
	}
	// Unknown run must yield an empty, non-nil slice (parity with MemStore).
	if got, err := s.Metrics("no-such-run"); err != nil || got == nil || len(got) != 0 {
		t.Errorf("unknown-run metrics = %v, %v (want empty non-nil)", got, err)
	}

	// --- Batched metrics round-trip (AppendMetrics) ---
	// A separate run keeps these assertions independent of the single-append run
	// above. The batch must persist every sample, in (ts, seq) order.
	batchRun := domain.ID("r-pg-batch")
	bbase := time.Now().UTC().Truncate(time.Microsecond)
	const batchN = 250 // > a single round-trip's worth; exercises the real batch
	batch := make([]domain.MetricSample, batchN)
	for i := range batch {
		batch[i] = domain.MetricSample{
			RunID:      batchRun,
			TS:         bbase.Add(time.Duration(i) * time.Millisecond),
			APIID:      "api-batch",
			StatusCode: 200 + i%3,
			LatencyMs:  float64(i),
			WorkerID:   "w-batch",
		}
	}
	if err := s.AppendMetrics(batch); err != nil {
		t.Fatalf("AppendMetrics: %v", err)
	}
	// Empty batch is a no-op, not an error.
	if err := s.AppendMetrics(nil); err != nil {
		t.Fatalf("AppendMetrics(nil): %v", err)
	}
	got, err := s.Metrics(batchRun)
	if err != nil {
		t.Fatalf("Metrics(batch): %v", err)
	}
	if len(got) != batchN {
		t.Fatalf("batched metrics count = %d, want %d", len(got), batchN)
	}
	if got[0].LatencyMs != 0 || got[batchN-1].LatencyMs != float64(batchN-1) {
		t.Errorf("batched metrics order/fields wrong: first=%+v last=%+v", got[0], got[batchN-1])
	}
	if got[10].WorkerID != "w-batch" || got[10].APIID != "api-batch" {
		t.Errorf("batched metric field round-trip mismatch: %+v", got[10])
	}
	// A batch containing a sample with no runId must fail atomically: nothing from
	// the batch is written (the transaction rolls back).
	badRun := domain.ID("r-pg-batch-bad")
	if err := s.AppendMetrics([]domain.MetricSample{
		{RunID: badRun, TS: bbase, StatusCode: 200},
		{RunID: "", TS: bbase, StatusCode: 200},
	}); err == nil {
		t.Error("AppendMetrics with empty runId should error")
	}
	if leftover, _ := s.Metrics(badRun); len(leftover) != 0 {
		t.Errorf("failed batch must roll back, got %d rows for %q", len(leftover), badRun)
	}

	// --- Findings round-trip (replace semantics) ---
	findings := []domain.Finding{
		{RunID: run.ID, Category: domain.FindingContract, Severity: domain.SeverityCritical, Description: "first", FirstSeen: base},
		{RunID: run.ID, Category: domain.FindingThreshold, Severity: domain.SeverityWarning, Description: "second", FirstSeen: base},
	}
	if err := s.SaveFindings(run.ID, findings); err != nil {
		t.Fatalf("SaveFindings: %v", err)
	}
	// Replace: a second save must wholly supersede the first set, not append.
	if err := s.SaveFindings(run.ID, findings[:1]); err != nil {
		t.Fatalf("SaveFindings (replace): %v", err)
	}
	gotFindings, err := s.Findings(run.ID)
	if err != nil {
		t.Fatalf("Findings: %v", err)
	}
	if len(gotFindings) != 1 || gotFindings[0].Category != domain.FindingContract || gotFindings[0].Description != "first" {
		t.Fatalf("findings round-trip = %+v", gotFindings)
	}
	if got, err := s.Findings("no-such-run"); err != nil || got == nil || len(got) != 0 {
		t.Errorf("unknown-run findings = %v, %v (want empty non-nil)", got, err)
	}
}
