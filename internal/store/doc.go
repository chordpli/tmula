// Package store persists experiments, runs, metrics and findings behind a
// single interface: an embedded local store (SQLite) for single-node mode and
// an external store (Postgres + time-series + queue) for distributed mode.
package store
