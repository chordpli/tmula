package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

func TestHandleImportConvertsToGraph(t *testing.T) {
	stub := func(data []byte, format string) (RunSpec, error) {
		if format != "auto" {
			t.Errorf("format = %q, want auto (default)", format)
		}
		if !strings.Contains(string(data), "openapi") {
			t.Errorf("body not passed through to importer: %q", data)
		}
		return RunSpec{
			Graph: domain.ScenarioGraph{
				ID:    "imported",
				Nodes: []domain.Node{{ID: "a", APITemplateID: "t_a"}},
			},
			Templates: map[domain.ID]domain.APITemplate{"t_a": {Method: "GET", Path: "/a"}},
			Start:     "a",
			MaxSteps:  5,
		}, nil
	}
	ts := httptest.NewServer(NewServer(load.NewRESTAdapter(time.Second), WithImporter(stub)).Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/import", "text/plain", strings.NewReader(`{"openapi":"3.0.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Graph struct {
			ID    string `json:"id"`
			Nodes []struct {
				ID            string `json:"id"`
				APITemplateID string `json:"apiTemplateId"`
			} `json:"nodes"`
		} `json:"graph"`
		Templates map[string]struct {
			Method string `json:"method"`
			Path   string `json:"path"`
		} `json:"templates"`
		Start    string `json:"start"`
		MaxSteps int    `json:"maxSteps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Graph.ID != "imported" || len(got.Graph.Nodes) != 1 || got.Graph.Nodes[0].ID != "a" {
		t.Errorf("graph = %+v, want imported/a", got.Graph)
	}
	if got.Start != "a" || got.MaxSteps != 5 {
		t.Errorf("start/maxSteps = %q/%d, want a/5", got.Start, got.MaxSteps)
	}
	if tpl, ok := got.Templates["t_a"]; !ok || tpl.Method != "GET" || tpl.Path != "/a" {
		t.Errorf("templates[t_a] = %+v (present=%v), want GET /a", tpl, ok)
	}
}

func TestHandleImportStatsIncluded(t *testing.T) {
	// When a stats-aware importer is wired it must win over the legacy one, and
	// its coverage stats must reach the response as the optional "stats" object
	// with stable camelCase keys (the UI's import coverage report contract).
	legacyCalled := false
	legacy := func([]byte, string) (RunSpec, error) { legacyCalled = true; return RunSpec{}, nil }
	stub := func(data []byte, format string) (RunSpec, *ImportStats, error) {
		if format != "auto" {
			t.Errorf("format = %q, want auto (default)", format)
		}
		if !strings.Contains(string(data), "GET /") {
			t.Errorf("body not passed through to importer: %q", data)
		}
		return RunSpec{
				Graph: domain.ScenarioGraph{
					ID:    "learned",
					Nodes: []domain.Node{{ID: "home", APITemplateID: "t_home"}},
				},
				Templates: map[domain.ID]domain.APITemplate{"t_home": {Method: "GET", Path: "/"}},
				Start:     "home",
				MaxSteps:  8,
			}, &ImportStats{
				Format:           "combined",
				Requests:         120,
				Skipped:          7,
				Sessions:         32,
				Clients:          21,
				DroppedEndpoints: 3,
				SkippedSamples: []ImportSkippedSample{
					{Line: 14, Text: "GARBAGE LINE", Reason: "unparsable line"},
				},
			}, nil
	}
	ts := httptest.NewServer(NewServer(load.NewRESTAdapter(time.Second), WithImporter(legacy), WithImporterStats(stub)).Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/import", "text/plain", strings.NewReader(`1.2.3.4 - - [01/Jan/2026:00:00:00 +0000] "GET / HTTP/1.1" 200 1`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Start string `json:"start"`
		Stats *struct {
			Format           string `json:"format"`
			Requests         int    `json:"requests"`
			Skipped          int    `json:"skipped"`
			Sessions         int    `json:"sessions"`
			Clients          int    `json:"clients"`
			DroppedEndpoints int    `json:"droppedEndpoints"`
			SkippedSamples   []struct {
				Line   int    `json:"line"`
				Text   string `json:"text"`
				Reason string `json:"reason"`
			} `json:"skippedSamples"`
		} `json:"stats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Start != "home" {
		t.Errorf("start = %q, want home", got.Start)
	}
	if got.Stats == nil {
		t.Fatal("stats missing from response; want the coverage report attached")
	}
	if got.Stats.Format != "combined" {
		t.Errorf("stats.format = %q, want combined (the detected log format must reach the UI)", got.Stats.Format)
	}
	if got.Stats.Requests != 120 || got.Stats.Skipped != 7 || got.Stats.Sessions != 32 ||
		got.Stats.Clients != 21 || got.Stats.DroppedEndpoints != 3 {
		t.Errorf("stats = %+v, want 120/7/32/21/3", got.Stats)
	}
	if len(got.Stats.SkippedSamples) != 1 ||
		got.Stats.SkippedSamples[0].Line != 14 ||
		got.Stats.SkippedSamples[0].Text != "GARBAGE LINE" ||
		got.Stats.SkippedSamples[0].Reason != "unparsable line" {
		t.Errorf("skippedSamples = %+v, want one {14, GARBAGE LINE, unparsable line}", got.Stats.SkippedSamples)
	}
	if legacyCalled {
		t.Error("legacy importer called although a stats-aware importer is wired")
	}
}

func TestHandleImportStatsNilOmitted(t *testing.T) {
	// A stats-aware importer may return nil stats (spec conversions have no
	// coverage to report); the "stats" key must then be absent, not null, so old
	// clients and the no-report UI path see the exact pre-stats response shape.
	stub := func([]byte, string) (RunSpec, *ImportStats, error) {
		return RunSpec{Start: "a", MaxSteps: 1}, nil, nil
	}
	ts := httptest.NewServer(NewServer(load.NewRESTAdapter(time.Second), WithImporterStats(stub)).Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/import", "text/plain", strings.NewReader(`{"openapi":"3.0.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["stats"]; ok {
		t.Errorf("response carries a stats key for nil stats: %s", raw["stats"])
	}
}

func TestHandleImportLegacyImporterHasNoStats(t *testing.T) {
	// The legacy ImportFunc wiring cannot produce stats; its response must stay
	// byte-compatible with the pre-stats contract (no "stats" key at all).
	stub := func([]byte, string) (RunSpec, error) {
		return RunSpec{Start: "a", MaxSteps: 1}, nil
	}
	ts := httptest.NewServer(NewServer(load.NewRESTAdapter(time.Second), WithImporter(stub)).Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/import", "text/plain", strings.NewReader(`{"openapi":"3.0.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["stats"]; ok {
		t.Errorf("legacy importer response carries a stats key: %s", raw["stats"])
	}
}

func TestHandleImportStatsImporterError(t *testing.T) {
	// A failing stats-aware importer reports 400 with the importer's message,
	// matching the legacy error contract.
	stub := func([]byte, string) (RunSpec, *ImportStats, error) {
		return RunSpec{}, nil, errors.New("no usable requests")
	}
	ts := httptest.NewServer(NewServer(load.NewRESTAdapter(time.Second), WithImporterStats(stub)).Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/import", "text/plain", strings.NewReader("garbage"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an importer failure", resp.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body.Error, "no usable requests") {
		t.Errorf("error = %q, want it to carry the importer's message", body.Error)
	}
}

func TestHandleImportNotConfigured(t *testing.T) {
	ts := httptest.NewServer(NewServer(load.NewRESTAdapter(time.Second)).Handler()) // no WithImporter
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/import", "text/plain", strings.NewReader("anything"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when no importer is wired", resp.StatusCode)
	}
}

func TestHandleImportEmptyBody(t *testing.T) {
	called := false
	stub := func([]byte, string) (RunSpec, error) { called = true; return RunSpec{}, nil }
	ts := httptest.NewServer(NewServer(load.NewRESTAdapter(time.Second), WithImporter(stub)).Handler())
	defer ts.Close()
	resp, err := http.Post(ts.URL+"/import", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an empty body", resp.StatusCode)
	}
	if called {
		t.Error("importer should not be called on an empty body")
	}
}
