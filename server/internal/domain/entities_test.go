package domain

import (
	"encoding/json"
	"fmt"
	"strings"
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

// TestCredentialPoolValidateLogin pins the CredLogin (token-minting) strategy
// branch: a login pool requires a non-empty LoginFlowID and a valid (or empty,
// defaulting to per-user) LoginScope. It is added cleanly beside the Source and
// bootstrap branches without disturbing them.
func TestCredentialPoolValidateLogin(t *testing.T) {
	login := ID("login")

	// A login pool with a flow id (and the default, empty scope) is valid.
	if err := (CredentialPool{Strategy: CredLogin, LoginFlowID: &login}).Validate(); err != nil {
		t.Errorf("valid login pool rejected: %v", err)
	}
	// An explicit per-user scope is valid.
	if err := (CredentialPool{Strategy: CredLogin, LoginFlowID: &login, LoginScope: LoginPerUser}).Validate(); err != nil {
		t.Errorf("login pool with per-user scope rejected: %v", err)
	}
	// An explicit shared scope is valid.
	if err := (CredentialPool{Strategy: CredLogin, LoginFlowID: &login, LoginScope: LoginShared}).Validate(); err != nil {
		t.Errorf("login pool with shared scope rejected: %v", err)
	}
	// Missing login flow id: rejected.
	if err := (CredentialPool{Strategy: CredLogin}).Validate(); err == nil {
		t.Error("login strategy without a login flow id should fail")
	}
	// Empty login flow id pointer value: rejected.
	empty := ID("")
	if err := (CredentialPool{Strategy: CredLogin, LoginFlowID: &empty}).Validate(); err == nil {
		t.Error("login strategy with an empty login flow id should fail")
	}
	// Unknown scope: rejected.
	if err := (CredentialPool{Strategy: CredLogin, LoginFlowID: &login, LoginScope: LoginScope("global")}).Validate(); err == nil {
		t.Error("login strategy with an unknown scope should fail")
	}
	// P8: a login pool MAY carry Entries — they are login-INPUT rows (username +
	// password), not pre-issued tokens, so virtual user i can log in as a different
	// account. A login pool with entries is now accepted.
	if err := (CredentialPool{Strategy: CredLogin, LoginFlowID: &login, Entries: []Credential{{Subject: "alice", Secret: "pw"}}}).Validate(); err != nil {
		t.Errorf("login strategy carrying login-input entries should be accepted (P8): %v", err)
	}
	// A login pool with NO entries is still valid: the single-identity login path.
	if err := (CredentialPool{Strategy: CredLogin, LoginFlowID: &login}).Validate(); err != nil {
		t.Errorf("login strategy without entries (single-identity) should be accepted: %v", err)
	}
	// A login pool MAY also carry a Source (an external file/env of login-input rows).
	// Its shape is validated like a pool source.
	if err := (CredentialPool{Strategy: CredLogin, LoginFlowID: &login, Source: &CredentialSourceRef{File: "users.csv", Format: "csv"}}).Validate(); err != nil {
		t.Errorf("login strategy carrying a login-input source should be accepted (P8): %v", err)
	}
	// A malformed login Source is still rejected for its shape.
	if err := (CredentialPool{Strategy: CredLogin, LoginFlowID: &login, Source: &CredentialSourceRef{Format: "csv"}}).Validate(); err == nil {
		t.Error("login strategy with a malformed source (neither file nor env) should fail")
	}
	// Entries AND Source together is a conflict for login too — pick one input source.
	if err := (CredentialPool{Strategy: CredLogin, LoginFlowID: &login, Entries: []Credential{{Subject: "a", Secret: "p"}}, Source: &CredentialSourceRef{File: "u.csv", Format: "csv"}}).Validate(); err == nil {
		t.Error("login strategy with both inline entries and a source should be rejected")
	}
}

// TestCredentialPoolLoginMarshalNoSecret confirms a login pool serializes only its
// non-secret declarative fields (the login flow id and scope) and never a secret —
// the minted token is acquired at runtime and never round-trips through a spec.
func TestCredentialPoolLoginMarshalNoSecret(t *testing.T) {
	login := ID("login")
	pool := CredentialPool{ID: "p", Strategy: CredLogin, LoginFlowID: &login, LoginScope: LoginShared}
	b, err := json.Marshal(pool)
	if err != nil {
		t.Fatalf("marshal login pool: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, "login") || !strings.Contains(out, "shared") {
		t.Errorf("login pool dropped its declarative reference: %s", out)
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "\"token\"") {
		t.Errorf("login pool carries a secret-shaped field: %s", out)
	}

	// P8: a login pool carrying login-INPUT rows must never serialize the password
	// (the row's Secret), but keeps the non-sensitive username (the row's Subject).
	multi := CredentialPool{ID: "p", Strategy: CredLogin, LoginFlowID: &login, Entries: []Credential{{Subject: "alice", Secret: "pw-secret-a"}}}
	mb, err := json.Marshal(multi)
	if err != nil {
		t.Fatalf("marshal multi-user login pool: %v", err)
	}
	if strings.Contains(string(mb), "pw-secret-a") {
		t.Errorf("multi-user login pool leaked a password: %s", mb)
	}
	if !strings.Contains(string(mb), "alice") {
		t.Errorf("multi-user login pool dropped the non-sensitive username: %s", mb)
	}

	// A non-login pool does not grow a loginFlowId/loginScope key (omitempty).
	plain := CredentialPool{ID: "p", Strategy: CredPool, Entries: []Credential{{Subject: "u0", Secret: "t"}}}
	pb, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal plain pool: %v", err)
	}
	if strings.Contains(string(pb), "loginFlowId") || strings.Contains(string(pb), "loginScope") {
		t.Errorf("plain pool grew a login key: %s", pb)
	}
}

// TestCredentialSourceRefMarshalNoSecret confirms a Source-based pool serializes
// its non-sensitive reference (path/var/format) and never a secret, and that an
// Entries-only pool serializes byte-identical to before the Source field existed
// (no spurious "source" key).
func TestCredentialSourceRefMarshalNoSecret(t *testing.T) {
	// A Source-based pool: the reference carries a file path and a format, never
	// a secret. The struct has no secret field by construction.
	srcPool := CredentialPool{
		ID:       "p",
		Strategy: CredPool,
		Source:   &CredentialSourceRef{File: "creds.csv", Format: "csv"},
	}
	b, err := json.Marshal(srcPool)
	if err != nil {
		t.Fatalf("marshal source pool: %v", err)
	}
	out := string(b)
	if !strings.Contains(out, "creds.csv") || !strings.Contains(out, "csv") {
		t.Errorf("source pool dropped its reference: %s", out)
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "token") {
		t.Errorf("source ref unexpectedly carries a secret-shaped field: %s", out)
	}

	// An Entries-only pool serializes exactly as it did before Source existed: no
	// "source" key appears.
	entPool := CredentialPool{
		ID:       "p",
		Strategy: CredPool,
		Entries:  []Credential{{Subject: "u0", Secret: "tok-0"}},
	}
	eb, err := json.Marshal(entPool)
	if err != nil {
		t.Fatalf("marshal entries pool: %v", err)
	}
	if strings.Contains(string(eb), "source") {
		t.Errorf("entries-only pool grew a source key: %s", eb)
	}
	if strings.Contains(string(eb), "tok-0") {
		t.Errorf("entries pool leaked a secret: %s", eb)
	}
}

// TestCredentialPoolValidateExactlyOne pins the exactly-one rule for the pool
// strategy: precisely one of Entries or Source must be present. Both-set and
// neither-set are rejected; either alone is accepted. The bootstrap path is
// unaffected.
func TestCredentialPoolValidateExactlyOne(t *testing.T) {
	entriesOnly := CredentialPool{Strategy: CredPool, Entries: []Credential{{Secret: "s"}}}
	if err := entriesOnly.Validate(); err != nil {
		t.Errorf("entries-only pool rejected: %v", err)
	}

	sourceOnly := CredentialPool{Strategy: CredPool, Source: &CredentialSourceRef{File: "c.csv", Format: "csv"}}
	if err := sourceOnly.Validate(); err != nil {
		t.Errorf("source-only pool rejected: %v", err)
	}

	both := CredentialPool{
		Strategy: CredPool,
		Entries:  []Credential{{Secret: "s"}},
		Source:   &CredentialSourceRef{File: "c.csv", Format: "csv"},
	}
	if err := both.Validate(); err == nil {
		t.Error("a pool with both entries and a source should be rejected")
	}

	neither := CredentialPool{Strategy: CredPool}
	if err := neither.Validate(); err == nil {
		t.Error("a pool with neither entries nor a source should be rejected")
	}

	// An invalid Source (within an otherwise source-only pool) is rejected.
	badSource := CredentialPool{Strategy: CredPool, Source: &CredentialSourceRef{Format: "csv"}}
	if err := badSource.Validate(); err == nil {
		t.Error("a pool whose source sets neither file nor env should be rejected")
	}

	// Bootstrap is unaffected: it ignores entries/source entirely.
	flow := ID("signup")
	if err := (CredentialPool{Strategy: CredBootstrapSignup, BootstrapFlowID: &flow}).Validate(); err != nil {
		t.Errorf("bootstrap pool rejected after exactly-one change: %v", err)
	}
}

// TestCredentialSourceRefValidate covers the reference's own shape rules:
// exactly one of File/Env, and a known format.
func TestCredentialSourceRefValidate(t *testing.T) {
	if err := (CredentialSourceRef{File: "c.csv", Format: "csv"}).Validate(); err != nil {
		t.Errorf("valid file ref rejected: %v", err)
	}
	if err := (CredentialSourceRef{Env: "TMULA_CREDS", Format: "tokens"}).Validate(); err != nil {
		t.Errorf("valid env ref rejected: %v", err)
	}
	if err := (CredentialSourceRef{Format: "csv"}).Validate(); err == nil {
		t.Error("a ref with neither file nor env should be rejected")
	}
	if err := (CredentialSourceRef{File: "c.csv", Env: "X", Format: "csv"}).Validate(); err == nil {
		t.Error("a ref with both file and env should be rejected")
	}
	if err := (CredentialSourceRef{File: "c.csv", Format: "yaml"}).Validate(); err == nil {
		t.Error("a ref with an unknown format should be rejected")
	}
	if err := (CredentialSourceRef{File: "c.csv"}).Validate(); err == nil {
		t.Error("a ref with an empty format should be rejected")
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
	c := Credential{Subject: "u1", Secret: "super-secret-token", Refresh: "super-secret-refresh", ExpiresIn: time.Hour}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(data) == "" || containsSecret(string(data)) {
		t.Fatalf("secret leaked into JSON: %s", data)
	}
	// Refresh and ExpiresIn are runtime-only (json:"-"): only Subject persists.
	if indexOf(string(data), "super-secret-refresh") >= 0 {
		t.Fatalf("refresh token leaked into JSON: %s", data)
	}
}

// TestCredentialStringRedactsSecrets pins that %v/%+v on a Credential — the
// shape a careless log line produces — never prints the access token NOR the
// refresh token, while still surfacing the non-sensitive subject.
func TestCredentialStringRedactsSecrets(t *testing.T) {
	c := Credential{Subject: "u1", Secret: "super-secret-token", Refresh: "super-secret-refresh"}
	for _, s := range []string{c.String(), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c)} {
		if indexOf(s, "super-secret-token") >= 0 {
			t.Errorf("access token leaked through Stringer: %s", s)
		}
		if indexOf(s, "super-secret-refresh") >= 0 {
			t.Errorf("refresh token leaked through Stringer: %s", s)
		}
		if indexOf(s, "u1") < 0 {
			t.Errorf("subject missing from Stringer output: %s", s)
		}
	}
	// A refresh-only credential (no access token) must still redact the refresh.
	refreshOnly := Credential{Subject: "u1", Refresh: "super-secret-refresh"}.String()
	if indexOf(refreshOnly, "super-secret-refresh") >= 0 {
		t.Errorf("refresh token leaked when Secret empty: %s", refreshOnly)
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
