package load

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

func TestRenderSubstitutesVariables(t *testing.T) {
	tmpl := domain.APITemplate{
		Protocol:        domain.ProtocolREST,
		Method:          "POST",
		Path:            "/users/{{.userId}}/orders",
		Headers:         map[string]string{"Authorization": "Bearer {{.token}}"},
		PayloadTemplate: `{"buyer":"{{.subject}}"}`,
	}
	cred := domain.Credential{Subject: "u-1", Secret: "tok-xyz"}
	req, err := Render(tmpl, "http://sut.local/", cred, map[string]string{"userId": "42"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if req.URL != "http://sut.local/users/42/orders" {
		t.Errorf("URL = %q", req.URL)
	}
	if req.Headers["Authorization"] != "Bearer tok-xyz" {
		t.Errorf("Authorization = %q", req.Headers["Authorization"])
	}
	if string(req.Body) != `{"buyer":"u-1"}` {
		t.Errorf("Body = %q", req.Body)
	}
}

func TestRenderMissingVariableErrors(t *testing.T) {
	tmpl := domain.APITemplate{Method: "GET", Path: "/x/{{.missing}}"}
	if _, err := Render(tmpl, "http://x", domain.Credential{}, nil); err == nil {
		t.Fatal("expected error for missing template variable")
	}
}

func TestRenderBasicAuthFunc(t *testing.T) {
	// {{basicAuth .subject .token}} is the http-basic route: the credential row is
	// username (subject) + password (token), encoded per RFC 7617.
	tmpl := domain.APITemplate{
		Method:  "GET",
		Path:    "/private",
		Headers: map[string]string{"Authorization": "Basic {{basicAuth .subject .token}}"},
	}
	cred := domain.Credential{Subject: "alice", Secret: "s3cr3t:with/odd=chars"}
	req, err := Render(tmpl, "http://sut.local", cred, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cr3t:with/odd=chars"))
	if req.Headers["Authorization"] != want {
		t.Errorf("Authorization = %q, want %q", req.Headers["Authorization"], want)
	}
}

func TestTemplateFuncsCoversBasicAuth(t *testing.T) {
	// TemplateFuncs is exported so template lint/guard helpers (e.g. the importer's
	// assertTemplateSafe) parse with the same function set the run path renders with.
	if _, ok := TemplateFuncs()["basicAuth"]; !ok {
		t.Fatal("TemplateFuncs() must include basicAuth")
	}
}

func TestRESTAdapterSendOK(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tmpl := domain.APITemplate{
		Method:          "POST",
		Path:            "/orders",
		Headers:         map[string]string{"Authorization": "Bearer {{.token}}"},
		PayloadTemplate: `{"x":1}`,
	}
	req, err := Render(tmpl, srv.URL, domain.Credential{Secret: "abc"}, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	a := NewRESTAdapter(2 * time.Second)
	resp, err := a.Send(context.Background(), req)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
	if resp.LatencyMs <= 0 {
		t.Errorf("latency should be > 0, got %v", resp.LatencyMs)
	}
	if !strings.Contains(string(resp.Body), `"ok":true`) {
		t.Errorf("body = %q", resp.Body)
	}
	if gotAuth != "Bearer abc" {
		t.Errorf("server saw Authorization = %q", gotAuth)
	}
	if gotBody != `{"x":1}` {
		t.Errorf("server saw body = %q", gotBody)
	}
}

func TestRESTAdapterAddsCorrelationHeaders(t *testing.T) {
	got := make(http.Header)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req := RenderedRequest{
		Method: "GET",
		URL:    srv.URL,
		Headers: map[string]string{
			HeaderRunID: "spoofed",
		},
		Correlation: RequestCorrelation{
			RunID:      "run-1",
			ScenarioID: "graph-1",
			NodeID:     "checkout",
			SessionID:  "user-7",
		},
	}
	if _, err := NewRESTAdapter(time.Second).Send(context.Background(), req); err != nil {
		t.Fatalf("send: %v", err)
	}
	assertHeader(t, got, HeaderRunID, "run-1")
	assertHeader(t, got, HeaderScenarioID, "graph-1")
	assertHeader(t, got, HeaderNodeID, "checkout")
	assertHeader(t, got, HeaderSessionID, "user-7")
}

func TestRESTAdapterOmitsUnsafeCorrelationHeaders(t *testing.T) {
	got := make(http.Header)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req := RenderedRequest{
		Method: "GET",
		URL:    srv.URL,
		Correlation: RequestCorrelation{
			RunID:      "run-1\r\nbad",
			ScenarioID: "",
			NodeID:     "node-1",
			SessionID:  "session\nbad",
		},
	}
	if _, err := NewRESTAdapter(time.Second).Send(context.Background(), req); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.Get(HeaderRunID) != "" {
		t.Errorf("%s should be omitted for unsafe value, got %q", HeaderRunID, got.Get(HeaderRunID))
	}
	if got.Get(HeaderScenarioID) != "" {
		t.Errorf("%s should be omitted for empty value, got %q", HeaderScenarioID, got.Get(HeaderScenarioID))
	}
	assertHeader(t, got, HeaderNodeID, "node-1")
	if got.Get(HeaderSessionID) != "" {
		t.Errorf("%s should be omitted for unsafe value, got %q", HeaderSessionID, got.Get(HeaderSessionID))
	}
}

func TestRESTAdapterServerErrorIsResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	a := NewRESTAdapter(2 * time.Second)
	resp, err := a.Send(context.Background(), RenderedRequest{Method: "GET", URL: srv.URL})
	if err != nil {
		t.Fatalf("5xx must be a Response, not an error: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestRESTAdapterTransportError(t *testing.T) {
	a := NewRESTAdapter(200 * time.Millisecond)
	// Connection refused: nothing is listening on this port.
	_, err := a.Send(context.Background(), RenderedRequest{Method: "GET", URL: "http://127.0.0.1:1/none"})
	if err == nil {
		t.Fatal("expected transport error")
	}
}

func TestRESTAdapterProtocol(t *testing.T) {
	if NewRESTAdapter(time.Second).Protocol() != domain.ProtocolREST {
		t.Fatal("REST adapter must report rest protocol")
	}
}

func assertHeader(t *testing.T, h http.Header, key, want string) {
	t.Helper()
	if got := h.Get(key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}
