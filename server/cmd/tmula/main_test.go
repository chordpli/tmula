package main

import (
	"path/filepath"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

func TestRunVersion(t *testing.T) {
	if err := run([]string{"--version"}); err != nil {
		t.Fatalf("run --version returned error: %v", err)
	}
}

func TestRunInvalidRole(t *testing.T) {
	if err := run([]string{"--role", "bogus"}); err == nil {
		t.Fatal("expected error for invalid role, got nil")
	}
}

// TestSetupStoreLocalInMemory: the zero-config local role gets an in-memory store
// and a no-op closer that touches no disk.
func TestSetupStoreLocalInMemory(t *testing.T) {
	st, closeStore, err := setupStore(domain.RoleLocal, "", "")
	if err != nil {
		t.Fatalf("setupStore: %v", err)
	}
	if st == nil {
		t.Fatal("store should not be nil")
	}
	closeStore() // must be safe and a no-op (no path configured)
}

// TestSetupStoreMasterFallsBackToMemory: master without a DSN must not fail; it
// degrades to an in-memory store so a misconfigured master still serves.
func TestSetupStoreMasterFallsBackToMemory(t *testing.T) {
	st, closeStore, err := setupStore(domain.RoleMaster, "", "")
	if err != nil {
		t.Fatalf("setupStore master/no-dsn: %v", err)
	}
	if st == nil {
		t.Fatal("store should not be nil on fallback")
	}
	closeStore()
}

// TestSetupStoreLocalSnapshotRoundTrip: with a --store path, the closer writes a
// snapshot and a fresh setup loads it, so a local restart keeps run history. A
// missing file on first start is not an error.
func TestSetupStoreLocalSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")

	first, closeFirst, err := setupStore(domain.RoleLocal, path, "")
	if err != nil {
		t.Fatalf("setupStore (first, missing file): %v", err)
	}
	if err := first.SaveRun(domain.RunExecution{ID: "r1", Status: domain.RunCompleted}); err != nil {
		t.Fatalf("save run: %v", err)
	}
	closeFirst() // writes the snapshot

	second, closeSecond, err := setupStore(domain.RoleLocal, path, "")
	if err != nil {
		t.Fatalf("setupStore (second, existing file): %v", err)
	}
	defer closeSecond()
	if r, err := second.GetRun("r1"); err != nil || r.Status != domain.RunCompleted {
		t.Errorf("reloaded run = %+v, %v; history did not survive restart", r, err)
	}
}

func TestResolveDSN(t *testing.T) {
	if got := resolveDSN("flag-dsn"); got != "flag-dsn" {
		t.Errorf("flag should win: got %q", got)
	}
	t.Setenv("TMULA_DB_DSN", "env-dsn")
	if got := resolveDSN(""); got != "env-dsn" {
		t.Errorf("env should be used when flag blank: got %q", got)
	}
	if got := resolveDSN("  flag  "); got != "  flag  " {
		t.Errorf("non-blank flag should win verbatim: got %q", got)
	}
}
