package domain

import (
	"math"
	"strings"
	"testing"
)

func TestRejectNaNWeight(t *testing.T) {
	g := ScenarioGraph{
		Nodes: []Node{{ID: "a"}, {ID: "b"}},
		Edges: []Edge{{From: "a", To: "b", Weight: math.NaN()}},
	}
	if err := g.Validate(); err == nil {
		t.Fatal("a NaN edge weight must be rejected")
	}
}

func TestLoadProfileShapeNonNegative(t *testing.T) {
	bad := LoadProfile{TargetAPIID: "x", Strategy: LoadRamp, Shape: ProfileShape{RampSeconds: -1}}
	if err := bad.Validate(); err == nil {
		t.Fatal("negative shape parameter must be rejected")
	}
	ok := LoadProfile{TargetAPIID: "x", Strategy: LoadRamp, Shape: ProfileShape{PeakConcurrency: 10}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}
}

func TestCredentialStringRedactsSecret(t *testing.T) {
	s := Credential{Subject: "u1", Secret: "super-secret"}.String()
	if strings.Contains(s, "super-secret") {
		t.Fatalf("secret leaked via String(): %s", s)
	}
	if !strings.Contains(s, "u1") {
		t.Fatalf("subject should be visible: %s", s)
	}
}

func TestBootstrapEmptyFlowIDRejected(t *testing.T) {
	empty := ID("")
	p := CredentialPool{Strategy: CredBootstrapSignup, BootstrapFlowID: &empty}
	if err := p.Validate(); err == nil {
		t.Fatal("a pointer to an empty bootstrapFlowId must be rejected")
	}
}
