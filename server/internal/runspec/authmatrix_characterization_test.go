package runspec_test

import (
	"testing"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/runspec"
)

// TestCredentialPoolCharacterization freezes the EXACT behavior of the run-path
// credential-pool validation (RunSpec.validateCredentialPool, reached via
// Validate) across every strategy × workers × source × flow combination, error
// string included. It is the safety net for the Phase 3 refactor (auth.go split
// and the authmatrix.go table): these assertions must pass byte-for-byte
// unchanged after the refactor, and any DELIBERATE rejection change (Phase 4)
// must update this table in lockstep with a code review.
//
// It drives the exported Validate() over a fully valid closed baseline so the
// ONLY thing that can fail is the credential-pool check.
func TestCredentialPoolCharacterization(t *testing.T) {
	const wantOK = "" // sentinel: Validate returns nil

	// sub-spec builders, kept inline so each case is self-contained.
	loginFlow := func() *runspec.LoginFlowSpec {
		return &runspec.LoginFlowSpec{
			Graph:     domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "tlogin"}}},
			Templates: map[domain.ID]domain.APITemplate{"tlogin": {Method: "POST", Path: "/login", Extract: map[string]string{"token": "access_token"}}},
			Start:     "login",
			TokenVar:  "token",
		}
	}
	loginID := domain.ID("login")
	signupWithTeardown := func() *domain.SignupFlow {
		return &domain.SignupFlow{
			Steps:    []domain.SignupStep{{ID: "signup", Method: "POST", Path: "/signup"}},
			Capture:  domain.SignupCapture{Token: "access_token"},
			Teardown: []domain.SignupStep{{ID: "remove", Method: "DELETE", Path: "/u/{{.subject}}"}},
		}
	}
	signupNoTeardown := func() *domain.SignupFlow {
		return &domain.SignupFlow{
			Steps:   []domain.SignupStep{{ID: "signup", Method: "POST", Path: "/signup"}},
			Capture: domain.SignupCapture{Token: "access_token"},
		}
	}
	sourceRef := func() *domain.CredentialSourceRef {
		return &domain.CredentialSourceRef{File: "creds.csv", Format: "csv"}
	}
	mintSpec := func() *domain.MintSpec {
		return &domain.MintSpec{Alg: domain.MintHS256, SecretEncoding: domain.MintEncodingRaw, Key: &domain.CredentialSourceRef{Env: "K"}, TTL: 3600_000_000_000}
	}
	execSpec := func() *domain.ExecSpec {
		return &domain.ExecSpec{Command: []string{"/bin/echo", "tok"}}
	}

	cases := []struct {
		name    string
		pool    *domain.CredentialPool
		flow    *runspec.LoginFlowSpec
		workers bool
		want    string
	}{
		{
			name: "nil pool is unauthenticated and valid",
			pool: nil,
			want: wantOK,
		},
		{
			name: "inline pool, no workers",
			pool: &domain.CredentialPool{ID: "p", Strategy: domain.CredPool, Entries: []domain.Credential{{Subject: "u0", Secret: "t0"}}},
			want: wantOK,
		},
		{
			name:    "inline pool + workers rejected",
			pool:    &domain.CredentialPool{ID: "p", Strategy: domain.CredPool, Entries: []domain.Credential{{Subject: "u0", Secret: "t0"}}},
			workers: true,
			want:    "api: an inline credential pool is not supported with distributed workers (only a reference-only source pool fans out; ship a credential source instead)",
		},
		{
			name: "source pool, no workers rejected",
			pool: &domain.CredentialPool{ID: "p", Strategy: domain.CredPool, Source: sourceRef()},
			want: "api: credential source must be resolved before running (the CLI resolves it at expand time; a distributed run with workers ships the reference instead)",
		},
		{
			name:    "source pool + workers allowed (distributed carve-out)",
			pool:    &domain.CredentialPool{ID: "p", Strategy: domain.CredPool, Source: sourceRef()},
			workers: true,
			want:    wantOK,
		},
		{
			name: "login pool with flow, no workers",
			pool: &domain.CredentialPool{ID: "p", Strategy: domain.CredLogin, LoginFlowID: &loginID},
			flow: loginFlow(),
			want: wantOK,
		},
		{
			name: "login pool without flow rejected",
			pool: &domain.CredentialPool{ID: "p", Strategy: domain.CredLogin, LoginFlowID: &loginID},
			flow: nil,
			want: "api: the \"login\" strategy needs a loginFlow describing how to mint a token",
		},
		{
			name:    "login pool + workers rejected (generic inline message)",
			pool:    &domain.CredentialPool{ID: "p", Strategy: domain.CredLogin, LoginFlowID: &loginID},
			flow:    loginFlow(),
			workers: true,
			want:    "api: an inline credential pool is not supported with distributed workers (only a reference-only source pool fans out; ship a credential source instead)",
		},
		{
			name: "bootstrap with teardown, no workers",
			pool: &domain.CredentialPool{ID: "p", Strategy: domain.CredBootstrapSignup, SignupFlow: signupWithTeardown()},
			want: wantOK,
		},
		{
			name: "bootstrap keep-accounts, no teardown, no workers",
			pool: &domain.CredentialPool{ID: "p", Strategy: domain.CredBootstrapSignup, SignupFlow: signupNoTeardown(), KeepAccounts: true},
			want: wantOK,
		},
		{
			name: "bootstrap no signupFlow rejected",
			pool: &domain.CredentialPool{ID: "p", Strategy: domain.CredBootstrapSignup, BootstrapFlowID: &loginID},
			want: "api: the \"bootstrap-signup\" strategy needs a signupFlow describing how to provision an account",
		},
		{
			name: "bootstrap no teardown no keep rejected",
			pool: &domain.CredentialPool{ID: "p", Strategy: domain.CredBootstrapSignup, SignupFlow: signupNoTeardown()},
			want: "api: the \"bootstrap-signup\" strategy provisions real accounts and must deprovision them: declare a teardown flow, or pass --keep-accounts to leave them in place",
		},
		{
			name:    "bootstrap + workers rejected (bootstrap-specific message)",
			pool:    &domain.CredentialPool{ID: "p", Strategy: domain.CredBootstrapSignup, SignupFlow: signupWithTeardown()},
			workers: true,
			want:    "api: the \"bootstrap-signup\" strategy is not supported with distributed workers (a bootstrap pool provisions per-node accounts and has no shared reference to fan out; distributed bootstrap is a follow-up)",
		},
		{
			name: "mint pool, no workers",
			pool: &domain.CredentialPool{ID: "p", Strategy: domain.CredMint, Mint: mintSpec()},
			want: wantOK,
		},
		{
			name:    "mint pool + workers rejected",
			pool:    &domain.CredentialPool{ID: "p", Strategy: domain.CredMint, Mint: mintSpec()},
			workers: true,
			want:    "api: the \"mint\" strategy is not supported with distributed workers yet (it signs per-node from a local key reference; distributed mint is a follow-up)",
		},
		{
			name: "exec pool, no workers",
			pool: &domain.CredentialPool{ID: "p", Strategy: domain.CredExec, Exec: execSpec()},
			want: wantOK,
		},
		{
			name:    "exec pool + workers rejected",
			pool:    &domain.CredentialPool{ID: "p", Strategy: domain.CredExec, Exec: execSpec()},
			workers: true,
			want:    "api: the \"exec\" strategy is not supported with distributed workers (it runs a local command per user; remote command execution is not fanned out)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := minimalSpec("http://127.0.0.1:1")
			s.CredentialPool = tc.pool
			s.LoginFlow = tc.flow
			if tc.pool != nil {
				s.Experiment.Params.AuthStrategy = tc.pool.Strategy
			}
			if tc.workers {
				s.Workers = []string{"127.0.0.1:65535"}
			}
			err := s.Validate()
			if tc.want == wantOK {
				if err != nil {
					t.Fatalf("Validate() = %q, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want %q", tc.want)
			}
			if err.Error() != tc.want {
				t.Errorf("Validate() error =\n  %q\nwant\n  %q", err.Error(), tc.want)
			}
		})
	}
}
