package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTargetEnvValidate(t *testing.T) {
	valid := TargetEnv{
		BaseURL:   "http://localhost:8080",
		Allowlist: []string{"localhost", "*.staging.internal"},
		RateCap:   RateCap{MaxRPS: 100, MaxConcurrency: 50},
		EnvClass:  EnvDev,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid TargetEnv rejected: %v", err)
	}

	bad := []TargetEnv{
		{Allowlist: []string{"x"}, RateCap: RateCap{1, 1}, EnvClass: EnvDev},               // no baseURL
		{BaseURL: "x", RateCap: RateCap{1, 1}, EnvClass: EnvDev},                           // empty allowlist
		{BaseURL: "x", Allowlist: []string{"x"}, EnvClass: EnvDev},                         // zero rate cap
		{BaseURL: "x", Allowlist: []string{"x"}, RateCap: RateCap{1, 1}, EnvClass: "prod"}, // invalid class
	}
	for i, te := range bad {
		if err := te.Validate(); err == nil {
			t.Errorf("bad TargetEnv[%d] passed validation", i)
		}
	}
}

func TestProdLockedFlagExists(t *testing.T) {
	if !EnvProdLocked.Valid() {
		t.Fatal("EnvProdLocked must be a valid env class")
	}
	te := TargetEnv{BaseURL: "x", Allowlist: []string{"x"}, RateCap: RateCap{1, 1}, EnvClass: EnvProdLocked}
	if err := te.Validate(); err != nil {
		t.Fatalf("prod-locked env should validate structurally: %v", err)
	}
}

func TestExperimentValidate(t *testing.T) {
	base := Experiment{
		Name:            "smoke",
		TargetEnvID:     "env1",
		ScenarioGraphID: "g1",
		Params:          ExperimentParams{VirtualUserCount: 10, DeviationRate: 0.1, AuthStrategy: CredPool},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid experiment rejected: %v", err)
	}

	overRate := base
	overRate.Params.DeviationRate = 1.5
	if err := overRate.Validate(); err == nil {
		t.Error("deviationRate > 1 should fail")
	}

	zeroUsers := base
	zeroUsers.Params.VirtualUserCount = 0
	if err := zeroUsers.Validate(); err == nil {
		t.Error("virtualUserCount 0 should fail")
	}
}

func TestCredentialPoolValidate(t *testing.T) {
	if err := (CredentialPool{Strategy: CredPool, Entries: []Credential{{Subject: "u1"}}}).Validate(); err != nil {
		t.Errorf("valid pool rejected: %v", err)
	}
	if err := (CredentialPool{Strategy: CredPool}).Validate(); err == nil {
		t.Error("pool strategy without entries should fail")
	}
	flow := ID("signup")
	if err := (CredentialPool{Strategy: CredBootstrapSignup, BootstrapFlowID: &flow}).Validate(); err != nil {
		t.Errorf("valid bootstrap pool rejected: %v", err)
	}
	if err := (CredentialPool{Strategy: CredBootstrapSignup}).Validate(); err == nil {
		t.Error("bootstrap strategy without flow should fail")
	}
}

// TestEdgeDependencyRoundTrip is the AC: Edge.Dependency must survive
// JSON serialization unchanged.
func TestEdgeDependencyRoundTrip(t *testing.T) {
	g := ScenarioGraph{
		ID:    "g1",
		Nodes: []Node{{ID: "a", APITemplateID: "t1"}, {ID: "b", APITemplateID: "t2"}},
		Edges: []Edge{{From: "a", To: "b", Weight: 1.0, Dependency: true}},
	}
	data, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ScenarioGraph
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Edges) != 1 || !got.Edges[0].Dependency {
		t.Fatalf("dependency edge not preserved through round-trip: %+v", got.Edges)
	}
	if got.Edges[0].From != "a" || got.Edges[0].To != "b" || got.Edges[0].Weight != 1.0 {
		t.Fatalf("edge fields not preserved: %+v", got.Edges[0])
	}
}

func TestScenarioGraphValidate(t *testing.T) {
	good := ScenarioGraph{
		Nodes: []Node{{ID: "a"}, {ID: "b"}},
		Edges: []Edge{{From: "a", To: "b", Weight: 1}},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}
	dup := ScenarioGraph{Nodes: []Node{{ID: "a"}, {ID: "a"}}}
	if err := dup.Validate(); err == nil {
		t.Error("duplicate node id should fail")
	}
	dangling := ScenarioGraph{Nodes: []Node{{ID: "a"}}, Edges: []Edge{{From: "a", To: "z"}}}
	if err := dangling.Validate(); err == nil {
		t.Error("edge to unknown node should fail")
	}
}

func TestCredentialSecretNotSerialized(t *testing.T) {
	c := Credential{Subject: "u1", Secret: "super-secret-token"}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) == "" || containsSecret(string(data)) {
		t.Fatalf("secret leaked into JSON: %s", data)
	}
}

func containsSecret(s string) bool {
	return len(s) > 0 && (indexOf(s, "super-secret-token") >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func TestReportShareExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	if (ReportShare{}).Expired(now) {
		t.Error("share with no expiry must not be expired")
	}
	if !(ReportShare{ExpiresAt: &past}).Expired(now) {
		t.Error("share past expiry must be expired")
	}
	if (ReportShare{ExpiresAt: &future}).Expired(now) {
		t.Error("share before expiry must not be expired")
	}
}
