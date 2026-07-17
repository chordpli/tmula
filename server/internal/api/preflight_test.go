package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
)

// postPreflight submits a spec to POST /auth/preflight and returns the HTTP status and
// decoded result.
func postPreflight(t *testing.T, srv *Server, spec RunSpec) (int, PreflightResult) {
	t.Helper()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/preflight", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var res PreflightResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	return rec.Code, res
}

// TestPreflightLoginSuccess drives the headline flow: configure a login auth and preflight
// it against a live (fake) IdP. The token is auto-detected from the body, the subject and
// source are reported, and only a 6-char prefix of the token is returned.
func TestPreflightLoginSuccess(t *testing.T) {
	sut, _ := newLoginSUT(0) // /login mints "tok-N", never expires
	defer sut.Close()
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))

	code, res := postPreflight(t, srv, specLogin(sut.URL, 1, ""))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !res.OK {
		t.Fatalf("preflight failed: %+v", res)
	}
	if res.Strategy != "login" {
		t.Errorf("strategy = %q, want login", res.Strategy)
	}
	if res.HTTPStatus != 200 {
		t.Errorf("httpStatus = %d, want 200", res.HTTPStatus)
	}
	// specLogin captures the token via an explicit TokenVar "token" extracting access_token.
	if res.TokenSource == "" {
		t.Error("tokenSource should name where the token came from")
	}
	if !strings.HasSuffix(res.TokenPrefix, "…") {
		t.Errorf("tokenPrefix %q should end with an ellipsis", res.TokenPrefix)
	}
	if res.Subject == "" {
		t.Error("subject should be reported (the login SUT returns user=principal)")
	}
}

// TestPreflightLoginWrongPassword proves an auth FAILURE is still a 200 with ok:false, a
// 401 httpStatus, and an enriched, actionable reason.
func TestPreflightLoginWrongPassword(t *testing.T) {
	sut := newRejectingLoginSUT() // /login always 401s
	defer sut.Close()
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))

	spec := specLogin(sut.URL, 1, "")
	spec.CredentialPool.Entries = []domain.Credential{{Subject: "alice", Secret: "wrong"}}

	code, res := postPreflight(t, srv, spec)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (an auth failure is still a 200 body)", code)
	}
	if res.OK {
		t.Fatal("a 401 login must be ok:false")
	}
	if res.HTTPStatus != 401 {
		t.Errorf("httpStatus = %d, want 401", res.HTTPStatus)
	}
	if !strings.Contains(res.Reason, "401") || !strings.Contains(strings.ToLower(res.Reason), "login") {
		t.Errorf("reason should be an actionable login failure, got %q", res.Reason)
	}
	if res.TokenPrefix != "" {
		t.Errorf("a failed preflight must carry no token prefix, got %q", res.TokenPrefix)
	}
}

// TestPreflightAllowlistEscapeBlocked proves the preflight cannot reach a host outside the
// target allowlist: a base URL whose host is not allowlisted is refused with 403 before any
// login traffic is sent.
func TestPreflightAllowlistEscapeBlocked(t *testing.T) {
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	spec := specLogin("http://evil.example.com", 1, "")
	// The allowlist covers only 127.0.0.1, but the target is evil.example.com.
	spec.TargetEnv.Allowlist = []string{"127.0.0.1"}

	code, res := postPreflight(t, srv, spec)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (a preflight must not escape the allowlist)", code)
	}
	if res.OK {
		t.Error("an allowlist-blocked preflight must not report ok:true")
	}
}

// TestPreflightSecretNeverLeaks is the AD-011 contract for the endpoint: the full token
// never appears anywhere in the response body, only its 6-char prefix.
func TestPreflightSecretNeverLeaks(t *testing.T) {
	sut, st := newLoginSUT(0)
	defer sut.Close()
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))

	b, _ := json.Marshal(specLogin(sut.URL, 1, ""))
	req := httptest.NewRequest(http.MethodPost, "/auth/preflight", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var res PreflightResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.OK {
		t.Fatalf("preflight failed: %+v", res)
	}
	// The login SUT minted exactly one token; its full value must not be in the body.
	st.mu.Lock()
	fullToken := "tok-" + itoa(st.minted)
	st.mu.Unlock()
	if strings.Contains(rec.Body.String(), fullToken) {
		t.Errorf("response leaked the full token %q:\n%s", fullToken, rec.Body.String())
	}
	if !strings.HasPrefix(fullToken, res.TokenPrefix[:len(res.TokenPrefix)-len("…")]) {
		t.Errorf("prefix %q should be the start of the token %q", res.TokenPrefix, fullToken)
	}
}

// TestPreflightPool parses entry 0 of a pre-supplied pool and reports it with source
// "pool", the entry-0 subject, a token prefix, and no HTTP status (no auth call is made).
// It drives doPreflight in-process so the inline entry secret (json:"-") survives — the
// HTTP wire strips it, which is exactly why the CLI preflights a pool in-process too.
func TestPreflightPool(t *testing.T) {
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	spec := specAuth("http://127.0.0.1:1", 3, twoEntryPool())
	guard := guardFor(t, spec.TargetEnv.BaseURL)

	res := srv.doPreflight(context.Background(), spec, guard)
	if !res.OK {
		t.Fatalf("pool preflight failed: %+v", res)
	}
	if res.TokenSource != "pool" {
		t.Errorf("tokenSource = %q, want pool", res.TokenSource)
	}
	if res.Subject != "u0" {
		t.Errorf("subject = %q, want u0 (entry 0)", res.Subject)
	}
	if res.TokenPrefix == "" {
		t.Error("pool preflight should carry a token prefix for entry 0")
	}
	if res.HTTPStatus != 0 {
		t.Errorf("a pool preflight makes no HTTP call, httpStatus should be omitted, got %d", res.HTTPStatus)
	}
}

// TestPreflightMint resolves the signing key and signs one token, reporting source "mint".
func TestPreflightMint(t *testing.T) {
	t.Setenv("TMULA_PREFLIGHT_MINT_KEY", "a-strong-hmac-secret")
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))

	spec := specAuth("http://127.0.0.1:1", 1, &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredMint,
		Mint: &domain.MintSpec{
			Alg:            domain.MintHS256,
			SecretEncoding: domain.MintEncodingRaw,
			Key:            &domain.CredentialSourceRef{Env: "TMULA_PREFLIGHT_MINT_KEY"},
			Subject:        "user-{{.userIndex}}",
			TTL:            time.Hour,
		},
	})

	code, res := postPreflight(t, srv, spec)
	if code != http.StatusOK || !res.OK {
		t.Fatalf("mint preflight failed: code=%d res=%+v", code, res)
	}
	if res.TokenSource != "mint" {
		t.Errorf("tokenSource = %q, want mint", res.TokenSource)
	}
	if res.Subject != "user-0" {
		t.Errorf("subject = %q, want user-0", res.Subject)
	}
	// A JWS starts with the base64url header "eyJ".
	if !strings.HasPrefix(res.TokenPrefix, "eyJ") {
		t.Errorf("mint tokenPrefix %q should look like a JWT header", res.TokenPrefix)
	}
}

// TestPreflightExecGated proves the exec strategy is refused (403) without the opt-in, like
// StartRun, so a preflight never runs an arbitrary command by default.
func TestPreflightExecGated(t *testing.T) {
	srv := NewServer(load.NewRESTAdapter(2 * time.Second)) // allowExec defaults to false
	spec := specAuth("http://127.0.0.1:1", 1, execPool())
	code, _ := postPreflight(t, srv, spec)
	if code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (exec preflight is gated)", code)
	}
}

// TestPreflightBootstrapRefusesWithoutTeardown proves a bootstrap preflight refuses
// politely when no teardown flow is declared, rather than leaking a probe account.
func TestPreflightBootstrapRefusesWithoutTeardown(t *testing.T) {
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	spec := specAuth("http://127.0.0.1:1", 1, &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredBootstrapSignup,
		SignupFlow: &domain.SignupFlow{
			Steps:   []domain.SignupStep{{ID: "signup", Method: "POST", Path: "/signup"}},
			Capture: domain.SignupCapture{Token: "access_token"},
		},
		KeepAccounts: true, // no teardown, but keep-accounts — preflight must still refuse
	})
	code, res := postPreflight(t, srv, spec)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a polite refusal, not an error)", code)
	}
	if res.OK {
		t.Fatal("a bootstrap preflight with no teardown must refuse (ok:false)")
	}
	if !strings.Contains(res.Reason, "teardown") {
		t.Errorf("refusal should explain the teardown requirement, got %q", res.Reason)
	}
}

// TestPreflightBootstrapTearsDownDespiteKeepAccounts proves a bootstrap preflight removes
// its probe account even when the run spec sets keep-accounts: the preflight forces
// teardown so it never strands the account it created (a keep-accounts spec would
// otherwise pass the HasTeardown gate and leak the account with ok:true).
func TestPreflightBootstrapTearsDownDespiteKeepAccounts(t *testing.T) {
	var signups, deletes int
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/signup":
			signups++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"tok-abc","subject":"u0"}`))
		case r.Method == http.MethodDelete:
			deletes++
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer sut.Close()

	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	spec := specAuth(sut.URL, 1, &domain.CredentialPool{
		ID:       "p",
		Strategy: domain.CredBootstrapSignup,
		SignupFlow: &domain.SignupFlow{
			Steps: []domain.SignupStep{{
				ID: "signup", Method: "POST", Path: "/signup", Body: `{"u":"u{{.userIndex}}"}`,
				Extract: map[string]string{"tok": "access_token", "uid": "subject"},
			}},
			Capture:  domain.SignupCapture{Token: "tok", Subject: "uid"},
			Teardown: []domain.SignupStep{{ID: "rm", Method: "DELETE", Path: "/u/{{.subject}}"}},
		},
		KeepAccounts: true, // the run would keep accounts, but a preflight must NOT strand one
	})

	code, res := postPreflight(t, srv, spec)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if !res.OK {
		t.Fatalf("bootstrap preflight should succeed: %+v", res)
	}
	if signups != 1 {
		t.Errorf("expected exactly one signup, got %d", signups)
	}
	if deletes != 1 {
		t.Errorf("teardown must fire despite keep-accounts so the probe account is not stranded; deletes = %d, want 1", deletes)
	}
}

// TestPreflightInvalidSpec rejects a request with no credential pool as a 400.
func TestPreflightInvalidSpec(t *testing.T) {
	srv := NewServer(load.NewRESTAdapter(2 * time.Second))
	spec := specFor("http://127.0.0.1:1", 1) // no credential pool
	code, _ := postPreflight(t, srv, spec)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no credential pool to preflight)", code)
	}
}
