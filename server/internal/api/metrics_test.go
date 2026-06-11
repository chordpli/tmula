package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// fakeProm answers every query_range with one small matrix series.
func fakeProm() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{},"values":[[1781085600,"0.5"],[1781085610,"0.9"]]}]}}`))
	}))
}

func metricsSpec(sutURL, promURL string) RunSpec {
	spec := specFor(sutURL, 2)
	spec.Metrics = &domain.MetricsSource{
		PrometheusURL: promURL,
		Queries:       []domain.MetricQuery{{Name: "cpu", Query: "node_cpu"}},
	}
	return spec
}

func TestRunAttachesServerMetrics(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	prom := fakeProm()
	defer prom.Close()

	rep := runInProcess(t, metricsSpec(sut.URL, prom.URL), 10*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("run status = %q, want completed", rep.Run.Status)
	}
	if rep.MetricsError != "" {
		t.Fatalf("MetricsError = %q, want none", rep.MetricsError)
	}
	if len(rep.ServerMetrics) != 1 || rep.ServerMetrics[0].Name != "cpu" {
		t.Fatalf("ServerMetrics = %+v, want one series named cpu", rep.ServerMetrics)
	}
	if got := rep.ServerMetrics[0].Points; len(got) != 2 || got[1].V != 0.9 {
		t.Errorf("points = %+v, want the fetched samples", got)
	}
}

func TestRunMetricsHostMustBeAllowlisted(t *testing.T) {
	sut := sutOK()
	defer sut.Close()

	// The spec's allowlist covers only 127.0.0.1; a Prometheus elsewhere is
	// refused by the same safety layer that guards the simulated traffic —
	// and the run itself still completes.
	rep := runInProcess(t, metricsSpec(sut.URL, "http://prom.elsewhere:9090"), 10*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("run status = %q, want completed (metrics are fail-soft)", rep.Run.Status)
	}
	if rep.MetricsError == "" || len(rep.ServerMetrics) != 0 {
		t.Errorf("want an allowlist MetricsError and no series, got err=%q series=%+v",
			rep.MetricsError, rep.ServerMetrics)
	}
}

func TestRunMetricsFetchFailureIsSoft(t *testing.T) {
	sut := sutOK()
	defer sut.Close()
	prom := fakeProm()
	promURL := prom.URL
	prom.Close() // nothing listens by run time

	rep := runInProcess(t, metricsSpec(sut.URL, promURL), 15*time.Second)
	if rep.Run.Status != domain.RunCompleted {
		t.Fatalf("run status = %q, want completed (metrics are fail-soft)", rep.Run.Status)
	}
	if rep.MetricsError == "" {
		t.Error("want a MetricsError describing the failed fetch")
	}
}
