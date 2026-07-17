package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// TestPoolTokensSurviveTheWire drives the console's ACTUAL wire path: a raw JSON
// RunSpec (entries carrying {"subject","token"}, exactly what buildRunSpec
// posts) is POSTed over HTTP, the run starts, and the SUT rejects any request
// without its subject's own bearer token. Before Credential.UnmarshalJSON the
// json:"-" tag silently dropped every posted token and the whole run 401'd —
// this test pins the decode end to end where an in-process struct test cannot.
func TestPoolTokensSurviveTheWire(t *testing.T) {
	var mu sync.Mutex
	unauthorized := 0
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth := r.Header.Get("Authorization")
		// tok-u0 / tok-u1 are the pool's tokens; anything else is a dropped secret.
		if auth != "Bearer tok-u0" && auth != "Bearer tok-u1" {
			unauthorized++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	cp := httptest.NewServer(NewServer(load.NewRESTAdapter(2 * time.Second)).Handler())
	defer cp.Close()

	// The raw browser wire shape — NOT a marshalled Go struct (whose json:"-"
	// would omit the tokens before they ever hit the wire).
	rawSpec := fmt.Sprintf(`{
		"experiment": {"name":"wire","targetEnvId":"e","scenarioGraphId":"g",
			"params":{"virtualUserCount":2,"authStrategy":"pool"}},
		"targetEnv": {"baseUrl":%q,"allowlist":["127.0.0.1"],
			"rateCap":{"maxRps":10000,"maxConcurrency":1000},"envClass":"dev"},
		"graph": {"id":"g","nodes":[{"id":"a","apiTemplateId":"ta"}]},
		"templates": {"ta":{"id":"ta","protocol":"rest","method":"GET","path":"/a",
			"headers":{"Authorization":"Bearer {{.token}}"}}},
		"start": "a", "maxSteps": 1, "userCount": 2, "seed": 1,
		"credentialPool": {"id":"web-pool","strategy":"pool",
			"entries":[{"subject":"u0","token":"tok-u0"},{"subject":"u1","token":"tok-u1"}]}
	}`, sut.URL)

	resp, err := http.Post(cp.URL+"/experiments", "application/json", strings.NewReader(rawSpec))
	if err != nil {
		t.Fatalf("POST /experiments: %v", err)
	}
	var created struct{ ID string }
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create (status %d): %v", resp.StatusCode, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || created.ID == "" {
		t.Fatalf("create status = %d, id = %q", resp.StatusCode, created.ID)
	}

	resp, err = http.Post(cp.URL+"/experiments/"+created.ID+"/run", "application/json", nil)
	if err != nil {
		t.Fatalf("POST run: %v", err)
	}
	var run struct {
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&run); err != nil {
		t.Fatalf("decode run (status %d): %v", resp.StatusCode, err)
	}
	resp.Body.Close()

	report := waitForStatus(t, cp.URL+"/runs/"+run.RunID+"/report", domain.RunCompleted, 5*time.Second)
	if report.Stats.Errors != 0 {
		t.Errorf("stats.Errors = %d, want 0 (posted pool tokens must reach the requests)", report.Stats.Errors)
	}
	mu.Lock()
	defer mu.Unlock()
	if unauthorized != 0 {
		t.Errorf("SUT saw %d unauthorized requests, want 0 — the wire dropped the pool tokens", unauthorized)
	}
}
