package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/obs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is a Postgres-backed Store for distributed mode. The local mode
// uses MemStore (in-memory + JSON snapshots); this implementation is the backend
// workers and the control plane share when running distributed.
//
// Entity bodies are persisted as JSONB alongside the few key columns each query
// filters or orders on, so the schema follows the domain types without a brittle
// column-per-field mapping. Credentials are never stored here (and domain types
// already tag secrets json:"-"), so no auth material reaches the database.
//
// Metrics are high-frequency and append-only: they live in their own table keyed
// by (run_id, ts) with a monotonic sequence for a stable total order. Producers
// fan in through internal/pipeline and land here via AppendMetrics, which batches
// many samples into one round-trip; Migrate additionally upgrades the metrics
// table to a TimescaleDB hypertable when that extension is available.
//
// A *pgxpool.Pool backs the store; pgxpool is safe for concurrent use, so
// PostgresStore is safe to share across goroutines without additional locking.
//
// The Store interface methods take no context (so the in-memory and Postgres
// backends share one signature and callers stay backend-agnostic), yet every
// pgx call needs one. Rather than detach each query with context.Background()
// (no deadline, no cancel), the store carries a base context tied to its own
// lifetime: derived from the constructor's context but independent of its
// deadline, and cancelled by Close. Queries thus stop promptly when the store
// is closed instead of running on after shutdown.
type PostgresStore struct {
	pool   *pgxpool.Pool
	ctx    context.Context
	cancel context.CancelFunc
}

// compile-time assertion that *PostgresStore implements Store.
var _ Store = (*PostgresStore)(nil)

// NewPostgresStore opens a connection pool to dsn and verifies connectivity with
// a ping. The returned store owns the pool; call Close to release it. The caller
// is expected to run Migrate once before use to create the schema.
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping postgres: %w", err)
	}
	// Base context for all queries: drop the constructor ctx's deadline (it may be
	// a short connect/ping timeout that should not bound later operations) but keep
	// it cancellable so Close stops in-flight work.
	baseCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &PostgresStore{pool: pool, ctx: baseCtx, cancel: cancel}, nil
}

// Close releases the underlying connection pool and cancels the store's base
// context, stopping any in-flight queries. It is safe to call once; the store
// must not be used afterward.
func (s *PostgresStore) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.pool != nil {
		s.pool.Close()
	}
}

// schemaDDL creates every table the store needs. It is idempotent
// (CREATE TABLE IF NOT EXISTS) so Migrate can run on every startup.
const schemaDDL = `
CREATE TABLE IF NOT EXISTS experiments (
    id   TEXT PRIMARY KEY,
    body JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
    id            TEXT PRIMARY KEY,
    experiment_id TEXT NOT NULL,
    body          JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS runs_experiment_id_idx ON runs (experiment_id);

CREATE TABLE IF NOT EXISTS run_stats (
    run_id TEXT PRIMARY KEY,
    body   JSONB NOT NULL
);

CREATE TABLE IF NOT EXISTS metrics (
    seq    BIGSERIAL PRIMARY KEY,
    run_id TEXT        NOT NULL,
    ts     TIMESTAMPTZ NOT NULL,
    body   JSONB       NOT NULL
);
CREATE INDEX IF NOT EXISTS metrics_run_ts_idx ON metrics (run_id, ts, seq);

CREATE TABLE IF NOT EXISTS findings (
    seq    BIGSERIAL PRIMARY KEY,
    run_id TEXT  NOT NULL,
    body   JSONB NOT NULL
);
CREATE INDEX IF NOT EXISTS findings_run_idx ON findings (run_id, seq);
`

// Migrate creates the schema if it does not already exist. It is idempotent and
// safe to call on every startup.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, schemaDDL); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	// Best-effort: when the TimescaleDB extension is present this upgrades the
	// metrics table to a TSDB hypertable for time-partitioned, high-frequency
	// writes. Plain Postgres lacks the function, so a failure is expected and
	// ignored — the table works unchanged as a regular relation.
	const hypertable = `SELECT create_hypertable('metrics', 'ts', if_not_exists => TRUE)`
	if _, err := s.pool.Exec(ctx, hypertable); err != nil {
		// Intentionally swallowed: missing extension or unsupported server.
		_ = err
	}
	return nil
}

// SaveExperiment inserts or replaces an experiment, keyed by its id.
func (s *PostgresStore) SaveExperiment(e domain.Experiment) error {
	if e.ID == "" {
		return fmt.Errorf("store: experiment id is required")
	}
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("store: marshal experiment %q: %w", e.ID, err)
	}
	ctx := s.ctx
	const q = `INSERT INTO experiments (id, body) VALUES ($1, $2)
               ON CONFLICT (id) DO UPDATE SET body = EXCLUDED.body`
	if _, err := s.pool.Exec(ctx, q, string(e.ID), body); err != nil {
		return fmt.Errorf("store: save experiment %q: %w", e.ID, err)
	}
	return nil
}

// GetExperiment returns an experiment or ErrNotFound.
func (s *PostgresStore) GetExperiment(id domain.ID) (domain.Experiment, error) {
	ctx := s.ctx
	var body []byte
	const q = `SELECT body FROM experiments WHERE id = $1`
	if err := s.pool.QueryRow(ctx, q, string(id)).Scan(&body); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Experiment{}, fmt.Errorf("%w: experiment %q", ErrNotFound, id)
		}
		return domain.Experiment{}, fmt.Errorf("store: get experiment %q: %w", id, err)
	}
	var e domain.Experiment
	if err := json.Unmarshal(body, &e); err != nil {
		return domain.Experiment{}, fmt.Errorf("store: unmarshal experiment %q: %w", id, err)
	}
	return e, nil
}

// SaveRun inserts or replaces a run, keyed by its id.
func (s *PostgresStore) SaveRun(r domain.RunExecution) error {
	if r.ID == "" {
		return fmt.Errorf("store: run id is required")
	}
	body, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("store: marshal run %q: %w", r.ID, err)
	}
	ctx := s.ctx
	const q = `INSERT INTO runs (id, experiment_id, body) VALUES ($1, $2, $3)
               ON CONFLICT (id) DO UPDATE SET experiment_id = EXCLUDED.experiment_id, body = EXCLUDED.body`
	if _, err := s.pool.Exec(ctx, q, string(r.ID), string(r.ExperimentID), body); err != nil {
		return fmt.Errorf("store: save run %q: %w", r.ID, err)
	}
	return nil
}

// GetRun returns a run or ErrNotFound.
func (s *PostgresStore) GetRun(id domain.ID) (domain.RunExecution, error) {
	ctx := s.ctx
	var body []byte
	const q = `SELECT body FROM runs WHERE id = $1`
	if err := s.pool.QueryRow(ctx, q, string(id)).Scan(&body); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.RunExecution{}, fmt.Errorf("%w: run %q", ErrNotFound, id)
		}
		return domain.RunExecution{}, fmt.Errorf("store: get run %q: %w", id, err)
	}
	var r domain.RunExecution
	if err := json.Unmarshal(body, &r); err != nil {
		return domain.RunExecution{}, fmt.Errorf("store: unmarshal run %q: %w", id, err)
	}
	return r, nil
}

// SaveStats inserts or replaces the aggregate stats for a run, keyed by run id.
func (s *PostgresStore) SaveStats(runID domain.ID, stats obs.Stats) error {
	if runID == "" {
		return fmt.Errorf("store: stats runId is required")
	}
	body, err := json.Marshal(stats)
	if err != nil {
		return fmt.Errorf("store: marshal stats for run %q: %w", runID, err)
	}
	ctx := s.ctx
	const q = `INSERT INTO run_stats (run_id, body) VALUES ($1, $2)
               ON CONFLICT (run_id) DO UPDATE SET body = EXCLUDED.body`
	if _, err := s.pool.Exec(ctx, q, string(runID), body); err != nil {
		return fmt.Errorf("store: save stats for run %q: %w", runID, err)
	}
	return nil
}

// Stats returns a run's aggregate stats or ErrNotFound.
func (s *PostgresStore) Stats(runID domain.ID) (obs.Stats, error) {
	ctx := s.ctx
	var body []byte
	const q = `SELECT body FROM run_stats WHERE run_id = $1`
	if err := s.pool.QueryRow(ctx, q, string(runID)).Scan(&body); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return obs.Stats{}, fmt.Errorf("%w: stats for run %q", ErrNotFound, runID)
		}
		return obs.Stats{}, fmt.Errorf("store: get stats for run %q: %w", runID, err)
	}
	var st obs.Stats
	if err := json.Unmarshal(body, &st); err != nil {
		return obs.Stats{}, fmt.Errorf("store: unmarshal stats for run %q: %w", runID, err)
	}
	return st, nil
}

// AppendMetric appends one metric sample to its run. Appends are independent
// inserts so concurrent workers never contend on a shared row.
func (s *PostgresStore) AppendMetric(m domain.MetricSample) error {
	if m.RunID == "" {
		return fmt.Errorf("store: metric runId is required")
	}
	body, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("store: marshal metric for run %q: %w", m.RunID, err)
	}
	ctx := s.ctx
	if _, err := s.pool.Exec(ctx, insertMetricQ, string(m.RunID), m.TS, body); err != nil {
		return fmt.Errorf("store: append metric for run %q: %w", m.RunID, err)
	}
	return nil
}

// insertMetricQ is the single-row insert reused for both AppendMetric and each
// queued statement of an AppendMetrics batch.
const insertMetricQ = `INSERT INTO metrics (run_id, ts, body) VALUES ($1, $2, $3)`

// AppendMetrics inserts a batch of samples with one network round-trip using
// pgx.Batch. The batch is sent inside a transaction so the whole set commits
// atomically — a failure on any row rolls back the rest, satisfying the
// all-or-nothing contract. An empty batch is a no-op. Samples are validated and
// marshalled up front so a bad sample never leaves a partial batch in flight.
func (s *PostgresStore) AppendMetrics(ms []domain.MetricSample) error {
	if len(ms) == 0 {
		return nil
	}
	bodies := make([][]byte, len(ms))
	for i := range ms {
		if ms[i].RunID == "" {
			return fmt.Errorf("store: metric[%d] runId is required", i)
		}
		body, err := json.Marshal(ms[i])
		if err != nil {
			return fmt.Errorf("store: marshal metric[%d] for run %q: %w", i, ms[i].RunID, err)
		}
		bodies[i] = body
	}

	ctx := s.ctx
	batch := &pgx.Batch{}
	for i := range ms {
		batch.Queue(insertMetricQ, string(ms[i].RunID), ms[i].TS, bodies[i])
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin metrics batch: %w", err)
	}
	// Rollback is a no-op once the tx has committed.
	defer func() { _ = tx.Rollback(ctx) }()

	br := tx.SendBatch(ctx, batch)
	// Drain one result per queued insert; the first error fails the whole batch.
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("store: append metric batch[%d] for run %q: %w", i, ms[i].RunID, err)
		}
	}
	if err := br.Close(); err != nil {
		return fmt.Errorf("store: close metrics batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit metrics batch: %w", err)
	}
	return nil
}

// Metrics returns the metric samples for a run ordered by timestamp then insertion
// sequence. The slice is never nil; an unknown run yields an empty slice.
func (s *PostgresStore) Metrics(runID domain.ID) ([]domain.MetricSample, error) {
	ctx := s.ctx
	const q = `SELECT body FROM metrics WHERE run_id = $1 ORDER BY ts, seq`
	rows, err := s.pool.Query(ctx, q, string(runID))
	if err != nil {
		return nil, fmt.Errorf("store: query metrics for run %q: %w", runID, err)
	}
	defer rows.Close()

	out := make([]domain.MetricSample, 0)
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("store: scan metric for run %q: %w", runID, err)
		}
		var m domain.MetricSample
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, fmt.Errorf("store: unmarshal metric for run %q: %w", runID, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read metrics for run %q: %w", runID, err)
	}
	return out, nil
}

// SaveFindings replaces the findings for a run. The delete and re-insert run in a
// single transaction so a reader never observes a partial replacement.
func (s *PostgresStore) SaveFindings(runID domain.ID, findings []domain.Finding) error {
	if runID == "" {
		return fmt.Errorf("store: findings runId is required")
	}
	ctx := s.ctx
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin findings tx for run %q: %w", runID, err)
	}
	// Rollback is a no-op once the tx has committed.
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM findings WHERE run_id = $1`, string(runID)); err != nil {
		return fmt.Errorf("store: clear findings for run %q: %w", runID, err)
	}
	const ins = `INSERT INTO findings (run_id, body) VALUES ($1, $2)`
	for i := range findings {
		body, err := json.Marshal(findings[i])
		if err != nil {
			return fmt.Errorf("store: marshal finding %d for run %q: %w", i, runID, err)
		}
		if _, err := tx.Exec(ctx, ins, string(runID), body); err != nil {
			return fmt.Errorf("store: insert finding %d for run %q: %w", i, runID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit findings for run %q: %w", runID, err)
	}
	return nil
}

// Findings returns the findings for a run ordered by insertion sequence. The
// slice is never nil; an unknown run yields an empty slice.
func (s *PostgresStore) Findings(runID domain.ID) ([]domain.Finding, error) {
	ctx := s.ctx
	const q = `SELECT body FROM findings WHERE run_id = $1 ORDER BY seq`
	rows, err := s.pool.Query(ctx, q, string(runID))
	if err != nil {
		return nil, fmt.Errorf("store: query findings for run %q: %w", runID, err)
	}
	defer rows.Close()

	out := make([]domain.Finding, 0)
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, fmt.Errorf("store: scan finding for run %q: %w", runID, err)
		}
		var f domain.Finding
		if err := json.Unmarshal(body, &f); err != nil {
			return nil, fmt.Errorf("store: unmarshal finding for run %q: %w", runID, err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read findings for run %q: %w", runID, err)
	}
	return out, nil
}
