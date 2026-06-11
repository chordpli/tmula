// Package metrics fetches server-side time series from Prometheus over a run's
// time window, so the report can place what the *servers* saw beside what the
// virtual users measured. It is strictly observability: callers treat every
// error as a note on the report, never as a run failure.
//
// It speaks the Prometheus HTTP API (GET /api/v1/query_range) with the
// standard library only, deliberately adding no client dependency.
package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// maxSeriesPerQuery bounds how many series one query may contribute: a query
// matching hundreds of label sets would otherwise swamp the report. Operators
// wanting more split into named queries.
const maxSeriesPerQuery = 5

// targetSamples sets the resolution: the window is divided into about this
// many steps (bounded below at 1s), keeping every fetched series small enough
// to ship inside a report.
const targetSamples = 60

// httpClient bounds each query on its own so one stuck connection cannot eat
// the whole fetch budget. Redirects are refused: the Prometheus URL passed the
// host allowlist, and following a redirect would let a compromised endpoint
// point the engine at a host that never did.
var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("metrics: redirects are not followed")
	},
}

// Fetch evaluates each query over [start, end] and returns the fetched series.
// Queries fail independently: the returned error (if any) joins the failures,
// while series from the queries that succeeded are still returned, so a report
// shows everything that could be correlated.
func Fetch(ctx context.Context, src domain.MetricsSource, start, end time.Time) ([]domain.MetricSeries, error) {
	if err := src.Validate(); err != nil {
		return nil, err
	}
	// Clamp a backwards window (clock skew, an EndedAt stamped before the
	// start) to a point query rather than sending start > end to Prometheus.
	if end.Before(start) {
		end = start
	}
	step := end.Sub(start) / targetSamples
	if step < time.Second {
		step = time.Second
	}

	var out []domain.MetricSeries
	var errs []error
	for _, q := range src.Queries {
		series, err := queryRange(ctx, src.PrometheusURL, q, start, end, step)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", q.Name, err))
			continue
		}
		out = append(out, series...)
	}
	return out, errors.Join(errs...)
}

// promEnvelope is the slice of the Prometheus query_range response we read.
type promEnvelope struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric map[string]string    `json:"metric"`
			Values [][2]json.RawMessage `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func queryRange(ctx context.Context, base string, q domain.MetricQuery, start, end time.Time, step time.Duration) ([]domain.MetricSeries, error) {
	u := strings.TrimSuffix(base, "/") + "/api/v1/query_range?" + url.Values{
		"query": {q.Query},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.FormatInt(int64(step.Seconds()), 10)},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var env promEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		// A gateway/proxy error (502, 404, ...) usually carries an HTML or
		// plain-text body; report the status rather than masking it behind a
		// JSON syntax error.
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("prometheus: %s", resp.Status)
		}
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || env.Status != "success" {
		msg := env.Error
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("prometheus: %s", msg)
	}

	results := env.Data.Result
	if len(results) > maxSeriesPerQuery {
		results = results[:maxSeriesPerQuery]
	}
	series := make([]domain.MetricSeries, 0, len(results))
	for _, r := range results {
		name := q.Name
		// With several label sets under one query, suffix the labels so the
		// series stay tell-apart-able in the report.
		if len(env.Data.Result) > 1 {
			name = q.Name + labelSuffix(r.Metric)
		}
		points := make([]domain.MetricPoint, 0, len(r.Values))
		for _, v := range r.Values {
			p, ok := parsePoint(v)
			if !ok {
				continue
			}
			points = append(points, p)
		}
		series = append(series, domain.MetricSeries{Name: name, Points: points})
	}
	return series, nil
}

// parsePoint reads one Prometheus sample pair: [unix-seconds, "value"]. A
// non-finite value ("NaN", "+Inf" — Prometheus emits both) is dropped:
// encoding/json cannot marshal NaN/Inf, so one such sample would otherwise
// break serving the entire report.
func parsePoint(v [2]json.RawMessage) (domain.MetricPoint, bool) {
	var sec float64
	if json.Unmarshal(v[0], &sec) != nil {
		return domain.MetricPoint{}, false
	}
	var raw string
	if json.Unmarshal(v[1], &raw) != nil {
		return domain.MetricPoint{}, false
	}
	val, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(val) || math.IsInf(val, 0) {
		return domain.MetricPoint{}, false
	}
	return domain.MetricPoint{TS: int64(sec * 1000), V: val}, true
}

// labelSuffix renders a label set as a stable "{k=v, ...}" suffix.
func labelSuffix(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
