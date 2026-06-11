package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// sutFailB serves 200 on every path except /b, which returns 500 — the
// minimal SUT that turns the shared a->b spec into contract findings on b.
func sutFailB() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/b") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
}

// findingWithRef returns the first finding with the given category and
// evidence ref, or nil.
func findingWithRef(rep Report, cat domain.FindingCategory, ref string) *domain.Finding {
	for i := range rep.Findings {
		if rep.Findings[i].Category == cat && rep.Findings[i].EvidenceRef == ref {
			return &rep.Findings[i]
		}
	}
	return nil
}

// TestClosedRunFindingsCarryEvidence drives the closed in-process path end to
// end: the contract finding on the failing node must carry representative
// sessions whose reproduce coordinates obey the closed-model seed arithmetic
// (session seed = run seed + pool index) and whose path ends at the failure.
func TestClosedRunFindingsCarryEvidence(t *testing.T) {
	sut := sutFailB()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 5) // Seed 1, users u0..u4, graph a -> b
	rep := runToReport(t, cp.URL, spec)

	f := findingWithRef(rep, domain.FindingContract, "b")
	if f == nil {
		t.Fatalf("no contract finding for b in %+v", rep.Findings)
	}
	if f.Evidence == nil {
		t.Fatal("contract finding has no evidence")
	}
	ev := f.Evidence
	if len(ev.Sessions) != 5 {
		t.Fatalf("sessions = %d, want all 5 failing users (cap not reached)", len(ev.Sessions))
	}
	for _, s := range ev.Sessions {
		idx, err := strconv.ParseInt(strings.TrimPrefix(s.SessionID, "u"), 10, 64)
		if err != nil {
			t.Fatalf("unexpected session id %q", s.SessionID)
		}
		// Closed model: user i is seeded spec.Seed + i; UserIndex is the offset
		// a reproduce command adds back to the run seed.
		if s.Seed != spec.Seed+idx || s.UserIndex != idx {
			t.Errorf("session %s coordinates = seed %d / index %d, want %d / %d", s.SessionID, s.Seed, s.UserIndex, spec.Seed+idx, idx)
		}
		if len(s.Path) == 0 || s.Path[len(s.Path)-1] != "b" {
			t.Errorf("session %s path = %v, want a walk ending at the failing node", s.SessionID, s.Path)
		}
		if s.StatusCode != http.StatusInternalServerError {
			t.Errorf("session %s status = %d, want 500", s.SessionID, s.StatusCode)
		}
	}
	if ev.StatusCounts[http.StatusInternalServerError] != 5 {
		t.Errorf("status counts = %+v, want {500:5}", ev.StatusCounts)
	}
}

// TestDistributedRunFindingsCarryEvidence drives the streaming distributed
// path across a real in-process gRPC worker: the master reconstructs each
// streamed result's reproduce coordinates from the worker's stable
// user-<global index> naming, so distributed findings stay diagnosable. The
// per-request path does not cross the wire — distributed evidence carries
// coordinates without journeys, the documented boundary.
func TestDistributedRunFindingsCarryEvidence(t *testing.T) {
	sut := sutFailB()
	defer sut.Close()

	w1, stop1 := startWorker(t)
	defer stop1()

	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 4) // Seed 1
	spec.Workers = []string{w1}
	rep := runToReport(t, cp.URL, spec)

	f := findingWithRef(rep, domain.FindingContract, "b")
	if f == nil {
		t.Fatalf("no contract finding for b in %+v", rep.Findings)
	}
	if f.Evidence == nil {
		t.Fatal("distributed contract finding has no evidence")
	}
	if len(f.Evidence.Sessions) != 4 {
		t.Fatalf("sessions = %d, want all 4 failing users", len(f.Evidence.Sessions))
	}
	for _, s := range f.Evidence.Sessions {
		idx, err := strconv.ParseInt(strings.TrimPrefix(s.SessionID, "user-"), 10, 64)
		if err != nil {
			t.Fatalf("unexpected worker session id %q", s.SessionID)
		}
		if s.Seed != spec.Seed+idx || s.UserIndex != idx {
			t.Errorf("session %s coordinates = seed %d / index %d, want %d / %d", s.SessionID, s.Seed, s.UserIndex, spec.Seed+idx, idx)
		}
	}
}

// TestRunReportJSONEvidenceShape pins the wire field naming on the API report:
// the evidence bundle and its session entries must serialize under the
// masker-safe names the share path depends on (no field name containing
// "session", which the deny-by-default masker would redact).
func TestRunReportJSONEvidenceShape(t *testing.T) {
	sut := sutFailB()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	spec := specFor(sut.URL, 2)
	rep := runToReport(t, cp.URL, spec)
	if f := findingWithRef(rep, domain.FindingContract, "b"); f == nil || f.Evidence == nil {
		t.Fatal("expected a contract finding with evidence")
	}

	// Re-fetch the raw JSON to inspect field names on the wire.
	resp, err := http.Get(cp.URL + "/runs/" + string(rep.Run.ID) + "/report")
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	defer resp.Body.Close()
	var raw strings.Builder
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		raw.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	body := raw.String()
	if !strings.Contains(body, `"evidence"`) || !strings.Contains(body, `"vus"`) {
		t.Errorf("report JSON missing evidence wire fields: %s", body)
	}
	if strings.Contains(body, `"sessionId"`) || strings.Contains(body, `"sessions"`) {
		t.Errorf("report JSON uses a field name containing 'session', which the share masker redacts: %s", body)
	}
}
