package demo

import (
	"bytes"
	"os"
	"testing"

	"github.com/chordpli/tmula/server/internal/importer"
)

// TestAccessLogStaysInSyncWithExamples guards the embedded copy: go:embed
// cannot reach above the package directory, so the demo carries its own copy of
// examples/imports/shop-access.log. This test fails the moment the two drift,
// so the demo always replays the same traffic the documented examples show.
func TestAccessLogStaysInSyncWithExamples(t *testing.T) {
	canonical, err := os.ReadFile("../../../examples/imports/shop-access.log")
	if err != nil {
		t.Fatalf("read canonical example log: %v", err)
	}
	if !bytes.Equal(AccessLog, canonical) {
		t.Error("embedded shop-access.log differs from examples/imports/shop-access.log; copy the canonical file over the embedded one")
	}
}

// TestAccessLogIsLearnable: the embedded log must always yield a usable
// behavior graph — it is the input `tmula demo` learns from, so a log edit
// that breaks the learner must fail here, not at demo time.
func TestAccessLogIsLearnable(t *testing.T) {
	sc, stats, err := importer.FromAccessLog(AccessLog)
	if err != nil {
		t.Fatalf("FromAccessLog(embedded): %v", err)
	}
	if stats.Requests == 0 || stats.Sessions == 0 {
		t.Errorf("stats = %+v, want usable requests and sessions", stats)
	}
	if sc.Graph == nil || len(sc.Graph.Nodes) == 0 {
		t.Fatal("learned scenario has no graph")
	}
	if sc.Open == nil {
		t.Error("learned scenario has no open-workload suggestion")
	}
}
