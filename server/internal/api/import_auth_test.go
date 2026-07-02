package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/runspec"
)

// loginPoolSpec is a RunSpec a login-strategy import would expand to: a login
// credential pool plus the standalone login flow whose body carries REPLACE_ME
// placeholders. The login flow's TokenVar is empty (E1 auto-detects).
func loginPoolSpec() RunSpec {
	flowID := domain.ID("login")
	return RunSpec{
		Graph:     domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "t_a"}}},
		Templates: map[domain.ID]domain.APITemplate{"t_a": {ID: "t_a", Protocol: domain.ProtocolREST, Method: "GET", Path: "/a"}},
		Start:     "a",
		MaxSteps:  3,
		CredentialPool: &domain.CredentialPool{
			ID:          "cli-pool",
			Strategy:    domain.CredLogin,
			LoginFlowID: &flowID,
		},
		LoginFlow: &runspec.LoginFlowSpec{
			Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t_login"}}},
			Templates: map[domain.ID]domain.APITemplate{"t_login": {ID: "t_login", Protocol: domain.ProtocolREST, Method: "POST", Path: "/oauth/token", PayloadTemplate: "grant_type=password&username=REPLACE_ME_USERNAME&password=REPLACE_ME_PASSWORD"}},
			Start:     "login",
			MaxSteps:  1,
		},
	}
}

// TestHandleImportReturnsLoginAuth asserts a login-strategy import surfaces the
// derived credentialPool (strategy login) and the standalone loginFlow (with the
// REPLACE_ME form body) in the /import response, so the web no longer throws the
// derived auth away.
func TestHandleImportReturnsLoginAuth(t *testing.T) {
	stub := func([]byte, string) (RunSpec, error) { return loginPoolSpec(), nil }
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
		CredentialPool *struct {
			Strategy    string `json:"strategy"`
			LoginFlowID string `json:"loginFlowId"`
		} `json:"credentialPool"`
		LoginFlow *struct {
			Start     string `json:"start"`
			Templates map[string]struct {
				Method          string `json:"method"`
				Path            string `json:"path"`
				PayloadTemplate string `json:"payloadTemplate"`
			} `json:"templates"`
		} `json:"loginFlow"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CredentialPool == nil {
		t.Fatal("response carries no credentialPool; the derived auth must reach the UI")
	}
	if got.CredentialPool.Strategy != string(domain.CredLogin) {
		t.Errorf("credentialPool.strategy = %q, want login", got.CredentialPool.Strategy)
	}
	if got.LoginFlow == nil {
		t.Fatal("response carries no loginFlow for a login-strategy import")
	}
	if got.LoginFlow.Start != "login" {
		t.Errorf("loginFlow.start = %q, want login", got.LoginFlow.Start)
	}
	tpl, ok := got.LoginFlow.Templates["t_login"]
	if !ok {
		t.Fatalf("loginFlow templates = %+v, want a t_login entry", got.LoginFlow.Templates)
	}
	if !strings.Contains(tpl.PayloadTemplate, "REPLACE_ME_PASSWORD") {
		t.Errorf("login body = %q, want the REPLACE_ME_PASSWORD placeholder", tpl.PayloadTemplate)
	}
}

// TestHandleImportReturnsSuggestedSignup asserts a register-op import surfaces
// the derived suggestedSignup (independent of the primary pool) in the response.
func TestHandleImportReturnsSuggestedSignup(t *testing.T) {
	stub := func([]byte, string) (RunSpec, error) {
		return RunSpec{
			Graph:     domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "t_a"}}},
			Templates: map[domain.ID]domain.APITemplate{"t_a": {ID: "t_a", Protocol: domain.ProtocolREST, Method: "GET", Path: "/a"}},
			Start:     "a",
			MaxSteps:  3,
			SuggestedSignup: &domain.SignupFlow{
				Steps: []domain.SignupStep{{
					ID:     "register",
					Method: "POST",
					Path:   "/register",
					Body:   `{"email":"tester+{{.userIndex}}@example.test","password":"REPLACE_ME_PASSWORD"}`,
				}},
				Teardown: []domain.SignupStep{{
					ID:     "teardown_register",
					Method: "DELETE",
					Path:   "/users/{{.subject}}",
				}},
			},
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
		SuggestedSignup *struct {
			Steps []struct {
				Method string `json:"method"`
				Path   string `json:"path"`
				Body   string `json:"body"`
			} `json:"steps"`
			Teardown []struct {
				Method string `json:"method"`
				Path   string `json:"path"`
			} `json:"teardown"`
		} `json:"suggestedSignup"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SuggestedSignup == nil {
		t.Fatal("response carries no suggestedSignup for a register-op import")
	}
	if len(got.SuggestedSignup.Steps) != 1 || got.SuggestedSignup.Steps[0].Path != "/register" {
		t.Fatalf("suggestedSignup steps = %+v, want a single POST /register", got.SuggestedSignup.Steps)
	}
	if !strings.Contains(got.SuggestedSignup.Steps[0].Body, "{{.userIndex}}") {
		t.Errorf("signup body = %q, want a per-VU unique identity", got.SuggestedSignup.Steps[0].Body)
	}
	if len(got.SuggestedSignup.Teardown) != 1 || got.SuggestedSignup.Teardown[0].Method != "DELETE" {
		t.Errorf("suggestedSignup teardown = %+v, want a single DELETE", got.SuggestedSignup.Teardown)
	}
}

// TestHandleImportNoAuthOmitsAllThree is the backward-compat control: a plain
// import with no auth must omit credentialPool, loginFlow and suggestedSignup
// entirely (the pre-P7 response shape).
func TestHandleImportNoAuthOmitsAllThree(t *testing.T) {
	stub := func([]byte, string) (RunSpec, error) {
		return RunSpec{
			Graph:     domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "t_a"}}},
			Templates: map[domain.ID]domain.APITemplate{"t_a": {ID: "t_a", Protocol: domain.ProtocolREST, Method: "GET", Path: "/a"}},
			Start:     "a",
			MaxSteps:  3,
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
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"credentialPool", "loginFlow", "suggestedSignup"} {
		if _, ok := raw[key]; ok {
			t.Errorf("no-auth import response carries a %q key: %s", key, raw[key])
		}
	}
}

// TestHandleImportNeverLeaksSecret is the AD-011 guard: even when a pool carries
// a captured secret (a HAR-extracted token in the entries), the marshaled import
// response must contain no real token/secret string — domain.Credential.Secret is
// json:"-", so the secret never crosses to the browser.
func TestHandleImportNeverLeaksSecret(t *testing.T) {
	const secret = "SUPER-SECRET-CAPTURED-TOKEN-xyz123"
	stub := func([]byte, string) (RunSpec, error) {
		return RunSpec{
			Graph:     domain.ScenarioGraph{ID: "g", Nodes: []domain.Node{{ID: "a", APITemplateID: "t_a"}}},
			Templates: map[domain.ID]domain.APITemplate{"t_a": {ID: "t_a", Protocol: domain.ProtocolREST, Method: "GET", Path: "/a"}},
			Start:     "a",
			MaxSteps:  3,
			CredentialPool: &domain.CredentialPool{
				ID:       "cli-pool",
				Strategy: domain.CredPool,
				// A captured HAR token rides as a pool entry secret; json:"-" must drop it.
				Entries: []domain.Credential{{Subject: "captured", Secret: secret}},
			},
		}, nil
	}
	ts := httptest.NewServer(NewServer(load.NewRESTAdapter(time.Second), WithImporter(stub)).Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/import", "text/plain", strings.NewReader(`{"openapi":"3.0.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatalf("import response leaked the captured secret; AD-011 requires it never cross to the browser:\n%s", body)
	}
	// The non-sensitive subject still rides, so the pool is recognizable in the UI.
	if !strings.Contains(string(body), "captured") {
		t.Errorf("import response dropped the non-sensitive subject; the pool should still surface: %s", body)
	}
}

// TestHandleImportReturnsAuthAdvisories asserts the /import response surfaces
// the importer's auth advisories (managed-IdP mint footgun, openIdConnect
// discovery pointer) so the UI can warn before the operator picks a strategy
// that cannot work.
func TestHandleImportReturnsAuthAdvisories(t *testing.T) {
	spec := loginPoolSpec()
	spec.AuthAdvisories = []domain.AuthAdvisory{
		{Code: "mint-managed-idp", Detail: "tenant.auth0.com"},
		{Code: "openidconnect-discovery", Detail: "https://idp/.well-known/openid-configuration"},
	}
	stub := func([]byte, string) (RunSpec, error) { return spec, nil }
	ts := httptest.NewServer(NewServer(load.NewRESTAdapter(time.Second), WithImporter(stub)).Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/import", "text/plain", strings.NewReader(`{"openapi":"3.0.0"}`))
	if err != nil {
		t.Fatalf("POST /import: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	var got struct {
		AuthAdvisories []struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		} `json:"authAdvisories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.AuthAdvisories) != 2 ||
		got.AuthAdvisories[0].Code != "mint-managed-idp" ||
		got.AuthAdvisories[0].Detail != "tenant.auth0.com" {
		t.Errorf("authAdvisories = %+v, want the spec's two advisories", got.AuthAdvisories)
	}
}
