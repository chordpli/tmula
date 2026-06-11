package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// fakeProm serves /api/v1/query_range, capturing each request and answering
// with a canned Prometheus matrix payload per query expression.
func fakeProm(t *testing.T, responses map[string]string) (*httptest.Server, *[]url.Values) {
	t.Helper()
	var seen []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		seen = append(seen, q)
		body, ok := responses[q.Get("query")]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","error":"unknown query"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

const cpuMatrix = `{"status":"success","data":{"resultType":"matrix","result":[
  {"metric":{"instance":"app:9100"},
   "values":[[1781085600,"0.31"],[1781085615,"0.62"]]}
]}}`

const connsMatrix = `{"status":"success","data":{"resultType":"matrix","result":[
  {"metric":{"db":"orders"},"values":[[1781085600,"12"]]},
  {"metric":{"db":"users"},"values":[[1781085600,"3"]]}
]}}`

func window() (time.Time, time.Time) {
	start := time.Unix(1781085600, 0).UTC()
	return start, start.Add(60 * time.Second)
}

func TestFetchReturnsSeriesPerQuery(t *testing.T) {
	srv, seen := fakeProm(t, map[string]string{
		"node_cpu_seconds": cpuMatrix,
		"db_connections":   connsMatrix,
	})
	src := domain.MetricsSource{
		PrometheusURL: srv.URL,
		Queries: []domain.MetricQuery{
			{Name: "cpu", Query: "node_cpu_seconds"},
			{Name: "conns", Query: "db_connections"},
		},
	}
	start, end := window()
	series, err := Fetch(context.Background(), src, start, end)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// One series for cpu, two for conns (labelled apart).
	if len(series) != 3 {
		t.Fatalf("series = %d, want 3: %+v", len(series), series)
	}
	if series[0].Name != "cpu" {
		t.Errorf("series[0].Name = %q, want cpu (single result keeps the bare query name)", series[0].Name)
	}
	if len(series[0].Points) != 2 || series[0].Points[1].V != 0.62 {
		t.Errorf("cpu points = %+v, want 2 points ending at 0.62", series[0].Points)
	}
	if series[0].Points[0].TS != 1781085600_000 {
		t.Errorf("point TS = %d, want unix milliseconds", series[0].Points[0].TS)
	}
	// Multi-series results carry their label sets so they stay tell-apart-able.
	if series[1].Name == series[2].Name {
		t.Errorf("multi-series names must differ, both %q", series[1].Name)
	}

	// The range covers the run window with a sane step.
	q := (*seen)[0]
	if got := q.Get("start"); got != strconv.Itoa(1781085600) {
		t.Errorf("start = %q, want run start in unix seconds", got)
	}
	if q.Get("end") == "" || q.Get("step") == "" {
		t.Errorf("end/step missing: %v", q)
	}
}

func TestFetchKeepsPartialResultsOnQueryError(t *testing.T) {
	srv, _ := fakeProm(t, map[string]string{"good": cpuMatrix})
	src := domain.MetricsSource{
		PrometheusURL: srv.URL,
		Queries: []domain.MetricQuery{
			{Name: "ok", Query: "good"},
			{Name: "broken", Query: "no_such"},
		},
	}
	start, end := window()
	series, err := Fetch(context.Background(), src, start, end)
	if err == nil {
		t.Fatal("expected an error for the failing query")
	}
	if len(series) != 1 || series[0].Name != "ok" {
		t.Errorf("partial series should survive a sibling failure, got %+v", series)
	}
}

func TestFetchUnreachableServer(t *testing.T) {
	src := domain.MetricsSource{
		PrometheusURL: "http://127.0.0.1:1", // nothing listens here
		Queries:       []domain.MetricQuery{{Name: "x", Query: "up"}},
	}
	start, end := window()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := Fetch(ctx, src, start, end); err == nil {
		t.Fatal("expected an error for an unreachable Prometheus")
	}
}

func TestFetchCapsSeriesPerQuery(t *testing.T) {
	// Eight series for one query: only the first maxSeriesPerQuery survive.
	big := `{"status":"success","data":{"resultType":"matrix","result":[`
	for i := 0; i < 8; i++ {
		if i > 0 {
			big += ","
		}
		big += `{"metric":{"i":"` + strconv.Itoa(i) + `"},"values":[[1781085600,"1"]]}`
	}
	big += `]}}`
	srv, _ := fakeProm(t, map[string]string{"wide": big})
	src := domain.MetricsSource{
		PrometheusURL: srv.URL,
		Queries:       []domain.MetricQuery{{Name: "w", Query: "wide"}},
	}
	start, end := window()
	series, err := Fetch(context.Background(), src, start, end)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(series) != maxSeriesPerQuery {
		t.Errorf("series = %d, want capped at %d", len(series), maxSeriesPerQuery)
	}
}

func TestFetchDropsNonFiniteSamplesAndRefusesRedirects(t *testing.T) {
	// "NaN"/"+Inf" samples must be dropped — encoding/json cannot marshal them,
	// so one such sample would otherwise 500 the whole report endpoint.
	const nanMatrix = `{"status":"success","data":{"resultType":"matrix","result":[
	  {"metric":{},"values":[[1781085600,"NaN"],[1781085610,"+Inf"],[1781085620,"1.5"]]}
	]}}`
	srv, _ := fakeProm(t, map[string]string{"spotty": nanMatrix})
	src := domain.MetricsSource{
		PrometheusURL: srv.URL,
		Queries:       []domain.MetricQuery{{Name: "s", Query: "spotty"}},
	}
	start, end := window()
	series, err := Fetch(context.Background(), src, start, end)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(series) != 1 || len(series[0].Points) != 1 || series[0].Points[0].V != 1.5 {
		t.Errorf("non-finite samples should be dropped, got %+v", series)
	}

	// A redirecting endpoint is refused rather than followed off-host.
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
	}))
	t.Cleanup(redir.Close)
	src.PrometheusURL = redir.URL
	if _, err := Fetch(context.Background(), src, start, end); err == nil {
		t.Error("expected an error for a redirecting metrics endpoint")
	}
}

func TestFetchReportsStatusOnNonJSONErrorBody(t *testing.T) {
	// A gateway in front of Prometheus answers 502 with an HTML body; the
	// error must carry the status, not a JSON syntax error that masks it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>Bad Gateway</body></html>"))
	}))
	t.Cleanup(srv.Close)
	src := domain.MetricsSource{
		PrometheusURL: srv.URL,
		Queries:       []domain.MetricQuery{{Name: "x", Query: "up"}},
	}
	start, end := window()
	_, err := Fetch(context.Background(), src, start, end)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("error should carry the HTTP status, got: %v", err)
	}
}

func TestMetricsSourceValidate(t *testing.T) {
	good := domain.MetricsSource{
		PrometheusURL: "http://prom:9090",
		Queries:       []domain.MetricQuery{{Name: "cpu", Query: "up"}},
	}
	if err := good.Validate(); err != nil {
		t.Errorf("valid source rejected: %v", err)
	}
	bad := []domain.MetricsSource{
		{PrometheusURL: "", Queries: good.Queries},
		{PrometheusURL: "prom:9090", Queries: good.Queries}, // no scheme
		{PrometheusURL: "ftp://p:1", Queries: good.Queries}, // wrong scheme
		{PrometheusURL: good.PrometheusURL},                 // no queries
		{PrometheusURL: good.PrometheusURL, Queries: []domain.MetricQuery{{Name: "", Query: "up"}}},
		{PrometheusURL: good.PrometheusURL, Queries: []domain.MetricQuery{{Name: "a", Query: ""}}},
		{PrometheusURL: good.PrometheusURL, Queries: []domain.MetricQuery{{Name: "a", Query: "up"}, {Name: "a", Query: "up"}}},
	}
	for i, src := range bad {
		if err := src.Validate(); err == nil {
			t.Errorf("bad source %d accepted: %+v", i, src)
		}
	}
}
