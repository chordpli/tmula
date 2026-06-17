package load

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

const (
	HeaderRunID      = "X-Tmula-Run-ID"
	HeaderScenarioID = "X-Tmula-Scenario-ID"
	HeaderNodeID     = "X-Tmula-Node-ID"
	HeaderSessionID  = "X-Tmula-Session-ID"
)

// Response is the protocol-agnostic result of one call to the system under test.
type Response struct {
	StatusCode int
	LatencyMs  float64
	Body       []byte
	// SetCookie carries the response's Set-Cookie header values (one per cookie).
	// It is populated only where a caller needs it (the findings-isolated setup
	// walks that auto-detect a credential); the hot request path leaves it nil so a
	// high-volume run does not retain cookie strings per response.
	SetCookie []string
}

// RequestCorrelation identifies the synthetic traffic in downstream logs and
// traces. The REST adapter turns these fields into X-Tmula-* headers.
type RequestCorrelation struct {
	RunID      domain.ID
	ScenarioID domain.ID
	NodeID     domain.ID
	SessionID  string
}

// RenderedRequest is a concrete request produced from an API template after
// variable substitution, ready for an adapter to send.
type RenderedRequest struct {
	Method      string
	URL         string
	Headers     map[string]string
	Body        []byte
	Correlation RequestCorrelation
}

// Adapter sends a rendered request to the system under test. REST is the first
// implementation; gRPC/WS can implement the same interface later.
type Adapter interface {
	Protocol() domain.Protocol
	Send(ctx context.Context, req RenderedRequest) (Response, error)
}

// Render substitutes variables into an API template's path, headers and payload
// using Go text/template syntax (e.g. {{.token}}). The credential is exposed as
// the "subject" and "token" variables in addition to vars.
func Render(tmpl domain.APITemplate, baseURL string, cred domain.Credential, vars map[string]string) (RenderedRequest, error) {
	ctx := make(map[string]string, len(vars)+2)
	for k, v := range vars {
		ctx[k] = v
	}
	ctx["subject"] = cred.Subject
	ctx["token"] = cred.Secret

	path, err := apply("path", tmpl.Path, ctx)
	if err != nil {
		return RenderedRequest{}, err
	}
	headers := make(map[string]string, len(tmpl.Headers))
	for k, v := range tmpl.Headers {
		hv, err := apply("header:"+k, v, ctx)
		if err != nil {
			return RenderedRequest{}, err
		}
		headers[k] = hv
	}
	var body []byte
	if tmpl.PayloadTemplate != "" {
		b, err := apply("payload", tmpl.PayloadTemplate, ctx)
		if err != nil {
			return RenderedRequest{}, err
		}
		body = []byte(b)
	}

	return RenderedRequest{
		Method:  tmpl.Method,
		URL:     strings.TrimRight(baseURL, "/") + path,
		Headers: headers,
		Body:    body,
	}, nil
}

func apply(name, text string, ctx map[string]string) (string, error) {
	if !strings.Contains(text, "{{") {
		return text, nil
	}
	t, err := template.New(name).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("load: parse template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, ctx); err != nil {
		return "", fmt.Errorf("load: render template %s: %w", name, err)
	}
	return buf.String(), nil
}

// maxResponseBytes bounds how much of a response body is read, so a hostile or
// buggy system under test cannot OOM the load generator.
const maxResponseBytes = 8 << 20 // 8 MiB

// RESTAdapter sends rendered requests over HTTP.
type RESTAdapter struct {
	client *http.Client
}

// NewRESTAdapter builds a REST adapter with the given per-request timeout.
func NewRESTAdapter(timeout time.Duration) *RESTAdapter {
	return &RESTAdapter{client: &http.Client{Timeout: timeout}}
}

// Protocol reports the protocol this adapter handles.
func (a *RESTAdapter) Protocol() domain.Protocol { return domain.ProtocolREST }

// Send performs the HTTP request and measures wall-clock latency. A non-2xx
// status is returned as a Response (not an error); only transport failures
// (DNS, connection, timeout) return an error.
func (a *RESTAdapter) Send(ctx context.Context, r RenderedRequest) (Response, error) {
	var bodyReader io.Reader
	if len(r.Body) > 0 {
		bodyReader = bytes.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, bodyReader)
	if err != nil {
		return Response{}, fmt.Errorf("load: build request: %w", err)
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	setCorrelationHeaders(req.Header, r.Correlation)

	start := time.Now()
	resp, err := a.client.Do(req)
	latency := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		return Response{}, fmt.Errorf("load: send: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Response{StatusCode: resp.StatusCode, LatencyMs: latency}, fmt.Errorf("load: read body: %w", err)
	}
	return Response{
		StatusCode: resp.StatusCode,
		LatencyMs:  latency,
		Body:       body,
		SetCookie:  resp.Header.Values("Set-Cookie"),
	}, nil
}

func setCorrelationHeaders(h http.Header, c RequestCorrelation) {
	setHeaderIfSafe(h, HeaderRunID, string(c.RunID))
	setHeaderIfSafe(h, HeaderScenarioID, string(c.ScenarioID))
	setHeaderIfSafe(h, HeaderNodeID, string(c.NodeID))
	setHeaderIfSafe(h, HeaderSessionID, c.SessionID)
}

func setHeaderIfSafe(h http.Header, key, value string) {
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return
	}
	h.Set(key, value)
}
