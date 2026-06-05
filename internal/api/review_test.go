package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/obs"
)

func TestKillAlreadyFinishedReturns409(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	resp := postJSON(t, cp.URL+"/experiments", specFor(sut.URL, 3))
	var created struct{ ID string }
	decode(t, resp, &created)
	resp = postJSON(t, cp.URL+"/experiments/"+created.ID+"/run", nil)
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)
	waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 3*time.Second)

	kr := postJSON(t, cp.URL+"/runs/"+run.RunID+"/kill", nil)
	defer kr.Body.Close()
	if kr.StatusCode != http.StatusConflict {
		t.Fatalf("killing a finished run = %d, want 409", kr.StatusCode)
	}
}

// TestReportJSONShape is the codegen-free drift guard: it pins the JSON key set
// of the report types so any Go field/tag change fails CI, prompting the
// matching update in web/src/api.ts (Report/Stats/Finding). Cheaper than
// generating the TS types and tolerant of the intentional TS/Go divergence.
func TestReportJSONShape(t *testing.T) {
	end := time.Unix(1, 0)
	rep := Report{
		Run:      domain.RunExecution{ID: "r", ExperimentID: "e", Mode: domain.RunLocal, Status: domain.RunCompleted, EndedAt: &end},
		Stats:    obs.Stats{StatusCounts: map[int]int{200: 1}},
		Findings: []domain.Finding{{RunID: "r", Category: domain.FindingContract, Severity: domain.SeverityCritical}},
	}
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertKeys(t, "Report", top, "run", "stats", "findings")

	var stats map[string]json.RawMessage
	_ = json.Unmarshal(top["stats"], &stats)
	assertKeys(t, "Stats", stats, "total", "errors", "timeouts", "errorRate", "statusCounts", "p50", "p95", "p99", "max")

	var findings []map[string]json.RawMessage
	_ = json.Unmarshal(top["findings"], &findings)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	assertKeys(t, "Finding", findings[0], "runId", "category", "severity")
}

func assertKeys(t *testing.T, what string, m map[string]json.RawMessage, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("%s JSON is missing key %q — web/src/api.ts is out of sync with the Go shape", what, k)
		}
	}
}
