package load

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/internal/domain"
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
