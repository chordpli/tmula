package cluster

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chordpli/tmula/server/internal/auth"
	"github.com/chordpli/tmula/server/internal/domain"
)

// TestCrossShardCredentialDeterminism is the core PR5 invariant: the UNION of
// every worker's per-shard credential assignment, keyed by GLOBAL index, EXACTLY
// equals the single-process assignment for the same total — including wrap-around
// when the user total exceeds the pool size. This is what makes distributed auth
// safe: every shard reconstructs the same index-deterministic PoolProvider, so
// splitting [0,total) across any number of workers never changes who any global
// index authenticates as.
func TestCrossShardCredentialDeterminism(t *testing.T) {
	t.Parallel()

	entries := []domain.Credential{
		{Subject: "u0", Secret: "tok-0"},
		{Subject: "u1", Secret: "tok-1"},
		{Subject: "u2", Secret: "tok-2"},
	}
	const n = 3 // pool size

	cases := []struct {
		total, workers int
	}{
		{total: 3, workers: 1},   // exact fit, single worker
		{total: 3, workers: 3},   // one user per worker
		{total: 10, workers: 3},  // wrap-around: total > N, uneven split (4,3,3)
		{total: 7, workers: 4},   // more workers than a clean split (2,2,2,1)
		{total: 5, workers: 9},   // more workers than users (empties skipped)
		{total: 100, workers: 7}, // large wrap-around
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("total=%d_workers=%d", tc.total, tc.workers), func(t *testing.T) {
			t.Parallel()

			// Single-process reference: one PoolProvider over the whole range.
			single, err := auth.NewPoolProvider(entries)
			if err != nil {
				t.Fatalf("provider: %v", err)
			}
			want := make([]string, tc.total)
			for g := 0; g < tc.total; g++ {
				cred, err := single.Acquire(context.Background(), g)
				if err != nil {
					t.Fatalf("single acquire %d: %v", g, err)
				}
				want[g] = cred.Secret
				// The single-process assignment must itself be entries[g % N]
				// (wrap-around), the contract the distributed path must match.
				if cred.Secret != entries[g%n].Secret {
					t.Fatalf("single-process index %d = %q, want entries[%d%%%d]=%q", g, cred.Secret, g, n, entries[g%n].Secret)
				}
			}

			// Distributed: each shard rebuilds its OWN provider (as a worker does
			// from the resolved source) and assigns its slice by global index.
			got := make([]string, tc.total)
			covered := make([]bool, tc.total)
			for _, sh := range splitUsers(tc.total, tc.workers) {
				shardProvider, err := auth.NewPoolProvider(entries)
				if err != nil {
					t.Fatalf("shard provider: %v", err)
				}
				for i := 0; i < sh.Count; i++ {
					g := sh.Offset + i
					if covered[g] {
						t.Fatalf("global index %d assigned by two shards (overlap)", g)
					}
					covered[g] = true
					cred, err := shardProvider.Acquire(context.Background(), g)
					if err != nil {
						t.Fatalf("shard acquire %d: %v", g, err)
					}
					got[g] = cred.Secret
				}
			}

			// The union must tile [0,total) with no gaps...
			for g := 0; g < tc.total; g++ {
				if !covered[g] {
					t.Fatalf("global index %d covered by no shard (gap)", g)
				}
			}
			// ...and match the single-process assignment exactly, including
			// wrap-around (entries[g % N]).
			for g := 0; g < tc.total; g++ {
				if got[g] != want[g] {
					t.Fatalf("global index %d: distributed=%q single-process=%q (must be entries[%d%%%d]=%q)", g, got[g], want[g], g, n, entries[g%n].Secret)
				}
			}
		})
	}
}

// TestWorkerSourceChecksumIsSecretFreeAndShared pins the cluster-side guard: two
// workers reading the SAME shared source in the SAME order compute the SAME
// secret-free checksum even when their files carry DIFFERENT secret bytes — so the
// digest detects a divergent/reordered source without ever leaking a secret. A
// source-less spec yields the empty checksum.
func TestWorkerSourceChecksumIsSecretFreeAndShared(t *testing.T) {
	t.Parallel()

	// Two workers, two files: identical subjects in identical order, different
	// secrets (a CSV carries subject,token; the token is the secret).
	dirA, dirB := t.TempDir(), t.TempDir()
	bodyA := "subject,token\nu0,secret-a-0\nu1,secret-a-1\nu2,secret-a-2\n"
	bodyB := "subject,token\nu0,DIFFERENT-0\nu1,DIFFERENT-1\nu2,DIFFERENT-2\n"
	if err := os.WriteFile(filepath.Join(dirA, "creds.csv"), []byte(bodyA), 0o600); err != nil {
		t.Fatalf("write A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "creds.csv"), []byte(bodyB), 0o600); err != nil {
		t.Fatalf("write B: %v", err)
	}

	spec := baseValidSpec()
	spec.CredentialSource = &domain.CredentialSourceRef{File: "creds.csv", Format: "csv"}

	wA := NewWorkerServer(WithCredentialRoot(dirA))
	wB := NewWorkerServer(WithCredentialRoot(dirB))

	sumA, err := wA.SourceChecksum(context.Background(), spec)
	if err != nil {
		t.Fatalf("worker A checksum: %v", err)
	}
	sumB, err := wB.SourceChecksum(context.Background(), spec)
	if err != nil {
		t.Fatalf("worker B checksum: %v", err)
	}
	if sumA == "" {
		t.Fatal("a source-backed spec must produce a non-empty checksum")
	}
	if sumA != sumB {
		t.Errorf("two workers sharing subjects+order must agree on the checksum despite different secrets: %q vs %q", sumA, sumB)
	}

	// A source-less spec has no pool to digest.
	if sum, err := wA.SourceChecksum(context.Background(), baseValidSpec()); err != nil || sum != "" {
		t.Errorf("a source-less spec must yield an empty checksum, got %q err=%v", sum, err)
	}
}
