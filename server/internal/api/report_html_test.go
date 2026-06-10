package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// runOnce creates and runs an experiment against sut, waits for it to complete,
// and returns the run id.
func runOnce(t *testing.T, cpURL, sutURL string, users int) string {
	t.Helper()
	resp := postJSON(t, cpURL+"/experiments", specFor(sutURL, users))
	var created struct{ ID string }
	decode(t, resp, &created)

	resp = postJSON(t, cpURL+"/experiments/"+created.ID+"/run", nil)
	var run struct {
		RunID string `json:"runId"`
	}
	decode(t, resp, &run)

	waitForStatus(t, cpURL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 3*time.Second)
	return run.RunID
}

func TestReportHTMLEndpoint(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	runID := runOnce(t, cp.URL, sut.URL, 5)

	resp, err := http.Get(cp.URL + "/runs/" + runID + "/report.html")
	if err != nil {
		t.Fatalf("get report.html: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report.html status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), runID) {
		t.Errorf("report html body does not contain run id %q", runID)
	}
	if !strings.Contains(string(body), "<!doctype html>") {
		t.Error("report html body is not a full HTML document")
	}
}

func TestReportHTMLMissingRun(t *testing.T) {
	cp, closeCP := newCP(t)
	defer closeCP()

	resp, err := http.Get(cp.URL + "/runs/run-does-not-exist/report.html")
	if err != nil {
		t.Fatalf("get report.html: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing run report.html = %d, want 404", resp.StatusCode)
	}
}

func TestCompareRunsEndpoint(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	a := runOnce(t, cp.URL, sut.URL, 3)
	b := runOnce(t, cp.URL, sut.URL, 5)

	resp, err := http.Get(cp.URL + "/runs/compare?a=" + a + "&b=" + b)
	if err != nil {
		t.Fatalf("get compare: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("compare status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), a) || !strings.Contains(string(body), b) {
		t.Error("compare body does not contain both run ids")
	}
}

func TestCompareRunsMissingRun(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	a := runOnce(t, cp.URL, sut.URL, 3)

	resp, err := http.Get(cp.URL + "/runs/compare?a=" + a + "&b=run-missing")
	if err != nil {
		t.Fatalf("get compare: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("compare with missing run = %d, want 404", resp.StatusCode)
	}
}

func TestCompareRunsSameRun(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	cp, closeCP := newCP(t)
	defer closeCP()

	a := runOnce(t, cp.URL, sut.URL, 3)

	resp, err := http.Get(cp.URL + "/runs/compare?a=" + a + "&b=" + a)
	if err != nil {
		t.Fatalf("get compare: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("compare a==b = %d, want 400", resp.StatusCode)
	}
}

func TestCompareRunsMissingParams(t *testing.T) {
	cp, closeCP := newCP(t)
	defer closeCP()

	resp, err := http.Get(cp.URL + "/runs/compare?a=only-a")
	if err != nil {
		t.Fatalf("get compare: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("compare with missing b = %d, want 400", resp.StatusCode)
	}
}
