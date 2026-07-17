package load

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// TestRunOnceCaptureExposesFinalBody proves RunOnceCapture returns the final
// step's raw response body so a caller can auto-detect a credential the flow did
// not explicitly extract.
func TestRunOnceCaptureExposesFinalBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"auto-xyz"}`))
	}))
	defer srv.Close()

	g := domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}}
	tmpls := map[domain.ID]domain.APITemplate{"t": {Method: "POST", Path: "/login"}} // no Extract
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	nodeTmpl, err := r.ResolveNodeTemplates(g)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	_, final, err := r.RunOnceCapture(context.Background(), g, nodeTmpl, "login", 4, VirtualUser{ID: "login-0"}, 1)
	if err != nil {
		t.Fatalf("runOnceCapture: %v", err)
	}
	token, _ := DetectCredential(final.Body, final.SetCookie)
	if token != "auto-xyz" {
		t.Errorf("auto-detected token = %q, want %q", token, "auto-xyz")
	}
}

// TestRunOnceCaptureExposesSetCookie proves the final response's Set-Cookie
// headers are threaded out so a cookie-session login can be auto-detected.
func TestRunOnceCaptureExposesSetCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "cookie-tok", Path: "/"})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	g := domain.ScenarioGraph{ID: "login", Nodes: []domain.Node{{ID: "login", APITemplateID: "t"}}}
	tmpls := map[domain.ID]domain.APITemplate{"t": {Method: "POST", Path: "/login"}}
	r := NewRunner(NewRESTAdapter(2*time.Second), srv.URL, tmpls)

	nodeTmpl, err := r.ResolveNodeTemplates(g)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	_, final, err := r.RunOnceCapture(context.Background(), g, nodeTmpl, "login", 4, VirtualUser{ID: "login-0"}, 1)
	if err != nil {
		t.Fatalf("runOnceCapture: %v", err)
	}
	token, _ := DetectCredential(final.Body, final.SetCookie)
	if token != "cookie-tok" {
		t.Errorf("auto-detected cookie token = %q, want %q", token, "cookie-tok")
	}
}
