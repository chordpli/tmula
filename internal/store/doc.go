// Package store persists experiments, runs, metrics and findings behind a
// single Store interface. The local mode ships a dependency-free in-memory
// implementation with optional JSON-file snapshots; the distributed mode can
// add an external store (Postgres + time-series) behind the same interface.
package store
