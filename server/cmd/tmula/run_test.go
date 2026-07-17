package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeEngine emulates the slice of the control-plane API that driveRun calls
// (create experiment, start run, poll report) and always reports the run with
// the given status and killReason. It lets the CLI's terminal-state handling be
// tested without a real engine or SUT.
func fakeEngine(status, killReason string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/experiments", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "exp-1"})
	})
	mux.HandleFunc("/api/experiments/exp-1/run", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"runId": "run-1"})
	})
	mux.HandleFunc("/api/runs/run-1/report", func(w http.ResponseWriter, _ *http.Request) {
		var rep cliReport
		rep.Run.ID = "run-1"
		rep.Run.Status = status
		rep.Run.KillReason = killReason
		_ = json.NewEncoder(w).Encode(rep)
	})
	return httptest.NewServer(mux)
}

// captureStdoutErr runs fn with os.Stdout redirected to a pipe and returns what
// it printed along with fn's error.
func captureStdoutErr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

// captureStdout is captureStdoutErr that fails the test if fn errors.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	out, err := captureStdoutErr(t, fn)
	if err != nil {
		t.Fatalf("runScenario: %v\noutput:\n%s", err, out)
	}
	return out
}

// TestRunSingleEndpointInProcess drives `tmula run --target ... --get /` end to
// end against an httptest SUT, using the in-process engine (no separate server).
func TestRunSingleEndpointInProcess(t *testing.T) {
	var hits int64
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	out := captureStdout(t, func() error {
		return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "3", "--json"})
	})

	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" {
		t.Errorf("status = %q, want completed", rep.Run.Status)
	}
	if rep.Stats.Total != 3 {
		t.Errorf("total = %d, want 3 (one request per user)", rep.Stats.Total)
	}
	if got := atomic.LoadInt64(&hits); got != 3 {
		t.Errorf("SUT hits = %d, want 3", got)
	}
}

// TestRunScenarioFileInProcess drives a run from a YAML scenario file.
func TestRunScenarioFileInProcess(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	file := filepath.Join(t.TempDir(), "scenario.yaml")
	doc := "target: " + sut.URL + "\nflow:\n  - id: a\n    request: GET /a\nusers: 4\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	out := captureStdout(t, func() error {
		return runScenario([]string{file, "--json"})
	})
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" || rep.Stats.Total != 4 {
		t.Errorf("got status=%q total=%d, want completed/4", rep.Run.Status, rep.Stats.Total)
	}
}

// TestRunScenarioFileAuthInProcess drives `tmula run scenario.yaml` where the
// scenario carries an auth block, and asserts the in-process run actually sends
// the pool's bearer tokens to the SUT — the end-to-end proof that a credential
// secret survives to the runtime (it cannot cross the wire, json:"-"), so the CLI
// authenticates without the web UI.
func TestRunScenarioFileAuthInProcess(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("Authorization")]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer sut.Close()

	file := filepath.Join(t.TempDir(), "scenario.yaml")
	doc := "target: " + sut.URL + "\n" +
		"users: 2\n" +
		"flow:\n" +
		"  - id: a\n" +
		"    request: GET /a\n" +
		"    headers:\n" +
		"      Authorization: \"Bearer {{.token}}\"\n" +
		"auth:\n" +
		"  users:\n" +
		"    - subject: alice\n" +
		"      token: tok-alice\n" +
		"    - subject: bob\n" +
		"      token: tok-bob\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	out := captureStdout(t, func() error {
		return runScenario([]string{file, "--json"})
	})
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" || rep.Stats.Total != 2 {
		t.Fatalf("got status=%q total=%d, want completed/2", rep.Run.Status, rep.Stats.Total)
	}

	mu.Lock()
	defer mu.Unlock()
	// Two users, two pool entries: user 0 -> tok-alice, user 1 -> tok-bob.
	if seen["Bearer tok-alice"] != 1 || seen["Bearer tok-bob"] != 1 {
		t.Errorf("auth headers seen = %v, want one each of tok-alice and tok-bob", seen)
	}
}

// TestRunLoginAuthInProcess drives a login (token-minting) scenario end to end
// against an httptest SUT in-process: the CLI mints a token from the login flow and
// the protected endpoint sees the minted bearer token.
func TestRunLoginAuthInProcess(t *testing.T) {
	var mu sync.Mutex
	var protectedAuths []string
	var loginHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		loginHits++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "minted-tok", "user": "svc"})
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		protectedAuths = append(protectedAuths, r.Header.Get("Authorization"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	sut := httptest.NewServer(mux)
	defer sut.Close()

	file := filepath.Join(t.TempDir(), "login.yaml")
	doc := "target: " + sut.URL + "\n" +
		"users: 2\n" +
		"flow:\n" +
		"  - id: a\n" +
		"    request: GET /a\n" +
		"    headers:\n" +
		"      Authorization: \"Bearer {{.token}}\"\n" +
		"auth:\n" +
		"  strategy: login\n" +
		"  login:\n" +
		"    flow:\n" +
		"      - id: login\n" +
		"        request: POST /login\n" +
		"        extract:\n" +
		"          token: access_token\n" +
		"          subject: user\n" +
		"    capture:\n" +
		"      token: token\n" +
		"      subject: subject\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	out := captureStdout(t, func() error { return runScenario([]string{file, "--json"}) })
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" || rep.Stats.Total != 2 {
		t.Fatalf("got status=%q total=%d, want completed/2", rep.Run.Status, rep.Stats.Total)
	}
	mu.Lock()
	defer mu.Unlock()
	if loginHits == 0 {
		t.Error("login endpoint was never hit; no token minted")
	}
	for _, a := range protectedAuths {
		if a != "Bearer minted-tok" {
			t.Errorf("protected endpoint saw %q, want the minted Bearer minted-tok", a)
		}
	}
}

// TestRunLoginRejectedAgainstRemoteEngine pins invariant 8: a login credential
// pool is refused against a remote --engine exactly like a static pool — the minted
// token is a json:"-" secret that cannot cross the wire, so the run must stay
// in-process.
func TestRunLoginRejectedAgainstRemoteEngine(t *testing.T) {
	eng := fakeEngine("completed", "")
	defer eng.Close()

	file := filepath.Join(t.TempDir(), "login.yaml")
	doc := "target: http://sut.invalid\n" +
		"users: 1\n" +
		"flow:\n" +
		"  - id: a\n" +
		"    request: GET /a\n" +
		"auth:\n" +
		"  strategy: login\n" +
		"  login:\n" +
		"    flow:\n" +
		"      - id: login\n" +
		"        request: POST /login\n" +
		"        extract:\n" +
		"          token: access_token\n" +
		"    capture:\n" +
		"      token: token\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	_, err := captureStdoutErr(t, func() error { return runScenario([]string{file, "--engine", eng.URL}) })
	if err == nil {
		t.Fatal("a login pool against a remote --engine must be rejected")
	}
	if !strings.Contains(err.Error(), "credential pool is not supported against a remote") {
		t.Errorf("rejection = %v, want the secret-cannot-cross-the-wire message", err)
	}
}

// TestRunFailOnFindings checks the CI gate: a SUT that always 5xxs produces
// findings, so --fail-on-findings makes the run return errFindings; without the
// flag the same run succeeds (findings are output, not a failure).
func TestRunFailOnFindings(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer sut.Close()

	_, err := captureStdoutErr(t, func() error {
		return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "5", "--fail-on-findings"})
	})
	if !errors.Is(err, errFindings) {
		t.Fatalf("err = %v, want errFindings", err)
	}

	// Same run without the flag must not error.
	captureStdout(t, func() error {
		return runScenario([]string{"--target", sut.URL, "--get", "/", "--users", "5"})
	})
}

func TestGatingFindings(t *testing.T) {
	fs := []cliFinding{{Severity: "warning"}, {Severity: "critical"}, {Severity: "critical"}}
	cases := []struct {
		failAny bool
		minSev  string
		want    int
	}{
		{false, "", 0},         // no gate requested
		{true, "", 3},          // any finding
		{false, "warning", 3},  // warning is the lowest level -> all
		{false, "critical", 2}, // criticals only
		{true, "critical", 2},  // severity refines the bool
	}
	for _, c := range cases {
		if got := gatingFindings(fs, c.failAny, c.minSev); got != c.want {
			t.Errorf("gatingFindings(failAny=%v, minSev=%q) = %d, want %d", c.failAny, c.minSev, got, c.want)
		}
	}
}

// TestFailOnSeverity uses a 400-only SUT (which yields a threshold WARNING
// finding but no criticals) to check the severity gate end to end.
func TestFailOnSeverity(t *testing.T) {
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer sut.Close()
	args := func(extra ...string) []string {
		return append([]string{"--target", sut.URL, "--get", "/", "--users", "5"}, extra...)
	}

	// critical-only gate: there are no criticals, so the run is not failed.
	captureStdout(t, func() error { return runScenario(args("--fail-on-severity", "critical")) })

	// warning gate: the threshold warning trips it.
	if _, err := captureStdoutErr(t, func() error { return runScenario(args("--fail-on-severity", "warning")) }); !errors.Is(err, errFindings) {
		t.Fatalf("warning gate err = %v, want errFindings", err)
	}

	// an invalid severity is rejected before the run starts.
	if err := runScenario(args("--fail-on-severity", "bogus")); err == nil {
		t.Error("invalid --fail-on-severity should error")
	}
}

func TestRunScenarioArgErrors(t *testing.T) {
	if err := runScenario([]string{}); err == nil {
		t.Error("no scenario file and no flags should error")
	}
	if err := runScenario([]string{"--get", "/"}); err == nil {
		t.Error("single-endpoint mode without --target should error")
	}
}

// TestRunFailedStatusNoKillReason covers the failed/killed terminal-state error:
// a "failed" run usually carries no kill reason, so the error must read "run
// failed" with no dangling colon; a "killed" run with a reason keeps it.
func TestRunFailedStatusNoKillReason(t *testing.T) {
	failed := fakeEngine("failed", "")
	defer failed.Close()
	_, err := captureStdoutErr(t, func() error {
		return runScenario([]string{"--target", "http://sut.invalid", "--get", "/", "--engine", failed.URL})
	})
	if err == nil {
		t.Fatal("failed run should return an error")
	}
	if got := err.Error(); got != "run failed" {
		t.Errorf("error = %q, want %q (no dangling colon for an empty kill reason)", got, "run failed")
	}

	killed := fakeEngine("killed", "circuit breaker tripped")
	defer killed.Close()
	_, err = captureStdoutErr(t, func() error {
		return runScenario([]string{"--target", "http://sut.invalid", "--get", "/", "--engine", killed.URL})
	})
	if err == nil {
		t.Fatal("killed run should return an error")
	}
	if got := err.Error(); got != "run killed: circuit breaker tripped" {
		t.Errorf("error = %q, want the reason appended", got)
	}
}

// TestHTTPClientHasTimeout guards fix: the report-poll loop must use a dedicated
// client with a per-request timeout, not http.DefaultClient (which has none), so
// a single stalled connection cannot hang the poll past the run timeout.
func TestHTTPClientHasTimeout(t *testing.T) {
	if httpClient == http.DefaultClient {
		t.Fatal("poll loop must not use http.DefaultClient (no timeout)")
	}
	if httpClient.Timeout <= 0 {
		t.Errorf("httpClient.Timeout = %v, want a positive per-request bound", httpClient.Timeout)
	}
}

// TestDoJSONReadErrorIsNetworkError covers fix: a body read that fails midway
// must surface as a clear "read response" network error, not fall through into a
// confusing JSON decode error. A handler that under-delivers its Content-Length
// (then the connection drops) triggers an io.ReadAll error.
func TestDoJSONReadErrorIsNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1024") // promise more than we send
		_, _ = io.WriteString(w, `{"id":"x"`)    // partial body, then close
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close() // abrupt close mid-body -> ReadAll errors
			}
		}
	}))
	defer srv.Close()

	var out struct {
		ID string `json:"id"`
	}
	err := getJSON(context.Background(), srv.URL, &out)
	if err == nil {
		t.Fatal("a truncated body should error")
	}
	if !strings.Contains(err.Error(), "read response") {
		t.Errorf("error = %q, want a clear read/network error (not a decode error)", err)
	}
}

// recordingEngine is fakeEngine that also captures the raw create-experiment
// request body, so a test can assert exactly what the CLI shipped to a remote
// engine (e.g. a reference-only credential source, never a secret).
func recordingEngine(t *testing.T, status string) (*httptest.Server, *[]byte) {
	t.Helper()
	var body []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/api/experiments", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = b
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "exp-1"})
	})
	mux.HandleFunc("/api/experiments/exp-1/run", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"runId": "run-1"})
	})
	mux.HandleFunc("/api/runs/run-1/report", func(w http.ResponseWriter, _ *http.Request) {
		var rep cliReport
		rep.Run.ID = "run-1"
		rep.Run.Status = status
		_ = json.NewEncoder(w).Encode(rep)
	})
	return httptest.NewServer(mux), &body
}

// TestRunSourcePoolShipsReferenceToEngine pins PR6: a SOURCE-backed auth pool is
// allowed against a remote --engine and ships only its reference-only SourceRef —
// the engine's workers resolve it locally. The create-experiment body the CLI
// posts must carry credentialPool.source (file + format) and NO inline entries or
// secret, and the credential file need not even be read by the CLI.
func TestRunSourcePoolShipsReferenceToEngine(t *testing.T) {
	eng, body := recordingEngine(t, "completed")
	defer eng.Close()

	dir := t.TempDir()
	// Note: we deliberately do NOT create creds.csv — the CLI must ship the
	// reference without reading the file when targeting a remote engine.
	file := filepath.Join(dir, "scenario.yaml")
	doc := "target: http://sut.invalid\n" +
		"users: 4\n" +
		"flow:\n" +
		"  - id: a\n" +
		"    request: GET /a\n" +
		"auth:\n" +
		"  source:\n" +
		"    file: creds.csv\n" +
		"    format: csv\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	if _, err := captureStdoutErr(t, func() error { return runScenario([]string{file, "--engine", eng.URL}) }); err != nil {
		t.Fatalf("a source pool against --engine must be accepted, got: %v", err)
	}

	if body == nil || len(*body) == 0 {
		t.Fatal("the CLI did not post a create-experiment body")
	}
	var sent struct {
		CredentialPool *struct {
			Strategy string `json:"strategy"`
			Entries  []struct {
				Subject string `json:"subject"`
			} `json:"entries"`
			Source *struct {
				File   string `json:"file"`
				Format string `json:"format"`
			} `json:"source"`
		} `json:"credentialPool"`
	}
	if err := json.Unmarshal(*body, &sent); err != nil {
		t.Fatalf("decode shipped spec: %v", err)
	}
	if sent.CredentialPool == nil {
		t.Fatal("the shipped spec carries no credential pool")
	}
	if sent.CredentialPool.Source == nil {
		t.Fatal("the shipped pool must carry a reference-only source")
	}
	if sent.CredentialPool.Source.File != "creds.csv" || sent.CredentialPool.Source.Format != "csv" {
		t.Errorf("source reference not shipped faithfully: %+v", sent.CredentialPool.Source)
	}
	if len(sent.CredentialPool.Entries) != 0 {
		t.Error("the shipped pool must NOT carry inline entries (no secret crosses the wire)")
	}
	// Defense in depth: the raw body must not contain a token field at all.
	if strings.Contains(string(*body), "\"token\"") {
		t.Errorf("the shipped body must contain no token bytes: %s", *body)
	}
}

// TestRunAuthExpiryNoteEndToEnd pins TASK 2 end to end: a SUT that rejects with
// 401 makes the run report carry the non-failing "auth may have expired" note,
// while a clean SUT carries none. The note is observability only — the 401 run is
// not failed by it (no --fail-on-findings here), and no auth finding is raised.
func TestRunAuthExpiryNoteEndToEnd(t *testing.T) {
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer rejecting.Close()

	out := captureStdout(t, func() error {
		return runScenario([]string{"--target", rejecting.URL, "--get", "/", "--users", "5", "--json"})
	})
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if len(rep.Notes) == 0 {
		t.Fatalf("a 401-clustered run must carry an auth-expiry note; report: %+v", rep)
	}
	var found bool
	for _, n := range rep.Notes {
		if strings.Contains(strings.ToLower(n), "auth") && strings.Contains(n, "401") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes %v should include the 401 auth-expiry note", rep.Notes)
	}

	clean := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer clean.Close()
	cleanOut := captureStdout(t, func() error {
		return runScenario([]string{"--target", clean.URL, "--get", "/", "--users", "5", "--json"})
	})
	var cleanRep cliReport
	if err := json.Unmarshal([]byte(cleanOut), &cleanRep); err != nil {
		t.Fatalf("parse clean report json: %v\n%s", err, cleanOut)
	}
	if len(cleanRep.Notes) != 0 {
		t.Errorf("a clean run must carry no notes, got %v", cleanRep.Notes)
	}
}

// authSeenSUT returns an httptest SUT that records each Authorization header it
// sees (under a mutex) and always 200s, plus accessors for the test to assert
// what bearer tokens reached it.
func authSeenSUT(t *testing.T) (*httptest.Server, func() map[string]int) {
	t.Helper()
	var mu sync.Mutex
	seen := map[string]int{}
	sut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.Header.Get("Authorization")]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(sut.Close)
	return sut, func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]int, len(seen))
		for k, v := range seen {
			out[k] = v
		}
		return out
	}
}

// authSourceScenario writes a scenario that sends "Bearer {{.token}}" but
// declares NO auth block, so the only credentials a run can carry come from the
// --auth-source flag. Returns the scenario file path.
func authSourceScenario(t *testing.T, target string, users int) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "scenario.yaml")
	doc := "target: " + target + "\n" +
		"users: " + itoa(users) + "\n" +
		"flow:\n" +
		"  - id: a\n" +
		"    request: GET /a\n" +
		"    headers:\n" +
		"      Authorization: \"Bearer {{.token}}\"\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return file
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestRunAuthSourceFileFlag pins TASK 1: --auth-source file:<path> attaches an
// external credential pool to a scenario that declares no auth, resolving the
// file in-process (the secret never crosses the wire) and authenticating the run.
// The format is inferred from the .csv extension.
func TestRunAuthSourceFileFlag(t *testing.T) {
	sut, seenFn := authSeenSUT(t)

	dir := t.TempDir()
	pool := filepath.Join(dir, "pool.csv")
	if err := os.WriteFile(pool, []byte("subject,token\nalice,tok-alice\nbob,tok-bob\n"), 0o644); err != nil {
		t.Fatalf("write pool: %v", err)
	}
	file := authSourceScenario(t, sut.URL, 2)

	out := captureStdout(t, func() error {
		return runScenario([]string{file, "--auth-source", "file:" + pool, "--json"})
	})
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" || rep.Stats.Total != 2 {
		t.Fatalf("got status=%q total=%d, want completed/2", rep.Run.Status, rep.Stats.Total)
	}
	seen := seenFn()
	if seen["Bearer tok-alice"] != 1 || seen["Bearer tok-bob"] != 1 {
		t.Errorf("auth headers seen = %v, want one each of tok-alice and tok-bob", seen)
	}
}

// TestRunAuthSourceEnvFlag pins that --auth-source env:VAR reads the pool from an
// environment variable. The tokens format (one secret per line) is explicit here.
func TestRunAuthSourceEnvFlag(t *testing.T) {
	sut, seenFn := authSeenSUT(t)
	t.Setenv("TMULA_TEST_CREDS", "tok-x\ntok-y\n")
	file := authSourceScenario(t, sut.URL, 2)

	out := captureStdout(t, func() error {
		return runScenario([]string{file, "--auth-source", "env:TMULA_TEST_CREDS", "--auth-format", "tokens", "--json"})
	})
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" || rep.Stats.Total != 2 {
		t.Fatalf("got status=%q total=%d, want completed/2", rep.Run.Status, rep.Stats.Total)
	}
	seen := seenFn()
	if seen["Bearer tok-x"] != 1 || seen["Bearer tok-y"] != 1 {
		t.Errorf("auth headers seen = %v, want one each of tok-x and tok-y", seen)
	}
}

// TestRunAuthSourceOverridesScenarioAuth pins the documented precedence: when the
// scenario ALSO declares an auth block, the --auth-source flag wins — the run
// authenticates with the flag's pool, not the file's.
func TestRunAuthSourceOverridesScenarioAuth(t *testing.T) {
	sut, seenFn := authSeenSUT(t)

	dir := t.TempDir()
	pool := filepath.Join(dir, "pool.csv")
	if err := os.WriteFile(pool, []byte("subject,token\nflagged,tok-from-flag\n"), 0o644); err != nil {
		t.Fatalf("write pool: %v", err)
	}
	file := filepath.Join(dir, "scenario.yaml")
	doc := "target: " + sut.URL + "\n" +
		"users: 1\n" +
		"flow:\n" +
		"  - id: a\n" +
		"    request: GET /a\n" +
		"    headers:\n" +
		"      Authorization: \"Bearer {{.token}}\"\n" +
		"auth:\n" +
		"  users:\n" +
		"    - subject: scenario\n" +
		"      token: tok-from-scenario\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	out := captureStdout(t, func() error {
		return runScenario([]string{file, "--auth-source", "file:" + pool, "--json"})
	})
	var rep cliReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("parse report json: %v\n%s", err, out)
	}
	if rep.Run.Status != "completed" {
		t.Fatalf("status = %q, want completed", rep.Run.Status)
	}
	seen := seenFn()
	if seen["Bearer tok-from-flag"] == 0 {
		t.Errorf("flag pool token never reached the SUT; seen = %v", seen)
	}
	if seen["Bearer tok-from-scenario"] != 0 {
		t.Errorf("scenario auth block was used despite --auth-source override; seen = %v", seen)
	}
}

// TestRunAuthSourceErrors covers the clear-failure paths: a malformed flag form
// (no scheme), an unknown scheme, a missing file, and an unset env var must each
// error before or during expansion rather than silently running unauthenticated.
func TestRunAuthSourceErrors(t *testing.T) {
	sut, _ := authSeenSUT(t)
	file := authSourceScenario(t, sut.URL, 1)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no scheme", []string{file, "--auth-source", "./pool.csv"}, "auth-source"},
		{"unknown scheme", []string{file, "--auth-source", "vault:/secret"}, "auth-source"},
		{"missing file", []string{file, "--auth-source", "file:/no/such/pool.csv"}, "credential source"},
		{"unset env", []string{file, "--auth-source", "env:TMULA_DEFINITELY_UNSET"}, "credential source"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := captureStdoutErr(t, func() error { return runScenario(c.args) })
			if err == nil {
				t.Fatalf("%s: expected an error, got nil", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("%s: error = %q, want it to mention %q", c.name, err, c.want)
			}
		})
	}
}

// TestRunInlinePoolStillRejectedAgainstEngine pins that an INLINE-entries pool is
// still refused against a remote --engine (its secret cannot cross the wire),
// even though a source pool now may.
func TestRunInlinePoolStillRejectedAgainstEngine(t *testing.T) {
	eng := fakeEngine("completed", "")
	defer eng.Close()

	file := filepath.Join(t.TempDir(), "scenario.yaml")
	doc := "target: http://sut.invalid\n" +
		"users: 1\n" +
		"flow:\n" +
		"  - id: a\n" +
		"    request: GET /a\n" +
		"auth:\n" +
		"  users:\n" +
		"    - subject: u0\n" +
		"      token: secret-0\n"
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	_, err := captureStdoutErr(t, func() error { return runScenario([]string{file, "--engine", eng.URL}) })
	if err == nil {
		t.Fatal("an inline credential pool against a remote --engine must be rejected")
	}
	if !strings.Contains(err.Error(), "credential pool is not supported against a remote") {
		t.Errorf("rejection should explain the secret cannot cross the wire, got: %v", err)
	}
}
