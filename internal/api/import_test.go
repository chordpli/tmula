package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/load"
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
