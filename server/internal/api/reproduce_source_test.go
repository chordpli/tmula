package api

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// sourceSpec returns a closed RunSpec authenticated by a reference-only file
// credential source plus distributed workers — the distributed-auth shape PR3
// allows. The source lives under root with the given filename.
func sourceSpec(file string) RunSpec {
	return RunSpec{
		Experiment: domain.Experiment{
			Name: "src", TargetEnvID: "e", ScenarioGraphID: "g",
			Params: domain.ExperimentParams{VirtualUserCount: 1, AuthStrategy: domain.CredPool},
		},
		TargetEnv: domain.TargetEnv{
			BaseURL:   "http://127.0.0.1:1",
			Allowlist: []string{"127.0.0.1"},
			RateCap:   domain.RateCap{MaxRPS: 100, MaxConcurrency: 10},
			EnvClass:  domain.EnvDev,
		},
		Graph:     domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "ta"}}},
		Templates: map[domain.ID]domain.APITemplate{"ta": {Method: "GET", Path: "/a"}},
		Start:     "a",
		MaxSteps:  1,
		UserCount: 8,
		Seed:      1,
		Workers:   []string{"127.0.0.1:65535"},
		CredentialPool: &domain.CredentialPool{
			ID:       "p",
			Strategy: domain.CredPool,
			Source:   &domain.CredentialSourceRef{File: file, Format: "tokens"},
		},
	}
}

// TestReproduceRebuildsDistributedSourcePrincipal pins the PR4 contract: a
// distributed-auth finding replays under the SAME principal the shard ran as —
// the reproduce path rebuilds the source-backed provider and re-acquires by the
// session's GLOBAL index, the identical pure Acquire the worker used. Global
// index 5 over a 3-token pool must resolve to entries[5 % 3].
func TestReproduceRebuildsDistributedSourcePrincipal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "creds.tokens"), []byte("tok-0\ntok-1\ntok-2\n"), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	srv := NewServer(load.NewRESTAdapter(0), WithCredentialRoot(dir))
	spec := sourceSpec("creds.tokens")

	// Validate confirms this is an accepted distributed-auth spec (the carve-out).
	if err := spec.Validate(); err != nil {
		t.Fatalf("a file source + workers spec must validate: %v", err)
	}

	// Session for global user index 5 → entries[5 % 3] == tok-2.
	sess := domain.EvidenceSession{SessionID: "user-5", UserIndex: 5, Seed: spec.Seed + 5}
	user, err := srv.sessionUser(t.Context(), spec, sess, nil)
	if err != nil {
		t.Fatalf("sessionUser: %v", err)
	}
	if user.ID != "user-5" {
		t.Errorf("session id = %q, want user-5", user.ID)
	}
	if user.Cred.Secret != "tok-2" {
		t.Errorf("reproduce principal secret = %q, want tok-2 (entries[5%%3])", user.Cred.Secret)
	}

	// A different global index resolves a different principal, proving the keying
	// is by global index, not a constant.
	sess0 := domain.EvidenceSession{SessionID: "user-3", UserIndex: 3, Seed: spec.Seed + 3}
	user0, err := srv.sessionUser(t.Context(), spec, sess0, nil)
	if err != nil {
		t.Fatalf("sessionUser idx 3: %v", err)
	}
	if user0.Cred.Secret != "tok-0" {
		t.Errorf("reproduce principal for index 3 = %q, want tok-0 (entries[3%%3])", user0.Cred.Secret)
	}
}

// TestReproduceUnavailableSourceIsTypedSentinel pins that a distributed-auth
// finding whose source no longer resolves server-side (the file is gone) is
// refused with errCredentialSourceUnavailable — a 410-class typed sentinel — not
// a panic and not a silent replay under the wrong principal.
func TestReproduceUnavailableSourceIsTypedSentinel(t *testing.T) {
	dir := t.TempDir() // exists, but the file does not
	srv := NewServer(load.NewRESTAdapter(0), WithCredentialRoot(dir))
	spec := sourceSpec("missing.tokens")

	sess := domain.EvidenceSession{SessionID: "user-0", UserIndex: 0, Seed: spec.Seed}
	_, err := srv.sessionUser(t.Context(), spec, sess, nil)
	if err == nil {
		t.Fatal("an unresolvable source must be refused, not replayed under a wrong principal")
	}
	if !errors.Is(err, errCredentialSourceUnavailable) {
		t.Fatalf("error must be errCredentialSourceUnavailable (maps to 410), got: %v", err)
	}
}
