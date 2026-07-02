package scenariofile

import (
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestExpandCarriesAuthAdvisories wires the importer's auth advisories (e.g. the
// mint-managed-idp footgun warning, the openIdConnect discovery pointer) through
// Expand onto the spec, so the /import response can surface them to the UI.
func TestExpandCarriesAuthAdvisories(t *testing.T) {
	s := Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
		AuthAdvisories: []domain.AuthAdvisory{
			{Code: "mint-managed-idp", Detail: "tenant.auth0.com"},
			{Code: "openidconnect-discovery", Detail: "https://idp/.well-known/openid-configuration"},
		},
	}
	spec, err := Expand(s)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(spec.AuthAdvisories) != 2 ||
		spec.AuthAdvisories[0].Code != "mint-managed-idp" ||
		spec.AuthAdvisories[0].Detail != "tenant.auth0.com" {
		t.Errorf("spec.AuthAdvisories = %+v, want the scenario's two advisories carried through", spec.AuthAdvisories)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("expanded spec failed validation: %v", err)
	}
}

// TestExpandNoAdvisoriesStaysEmpty pins the default: a scenario without
// advisories expands with none (the field is omitempty on the wire).
func TestExpandNoAdvisoriesStaysEmpty(t *testing.T) {
	spec, err := Expand(Scenario{
		Target: "http://h:1",
		Flow:   []Step{{ID: "a", Request: "GET /a"}},
	})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(spec.AuthAdvisories) != 0 {
		t.Errorf("AuthAdvisories = %+v, want empty", spec.AuthAdvisories)
	}
}
