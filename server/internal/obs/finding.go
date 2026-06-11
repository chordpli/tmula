package obs

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/chordpli/tmula/server/internal/domain"
)

// RequestObservation is one observed request with enough context to classify a
// finding (notably whether the input was deliberately mutated).
type RequestObservation struct {
	APIID      domain.ID
	StatusCode int
	LatencyMs  float64
	ErrorClass string // "", "transport", "timeout", "assertion", ...
	Mutated    bool
	TS         time.Time

	// Optional session context for evidence. All of it is best-effort: a
	// producer that cannot attribute requests to sessions (the worker-side
	// Summary path retains no per-request data at all) leaves these zero and
	// classification is unaffected — only the findings' evidence bundles lose
	// their per-session representatives.
	//
	// SessionID is the virtual-user/session label (the X-Tmula-Session-ID
	// correlation value). Seed is the session's walk seed and UserIndex the
	// offset deriving it from the run seed (run seed + UserIndex == Seed);
	// both are meaningful only when SessionID is non-empty. Persona is the
	// segment the session was drawn from ("" without a mix).
	SessionID string
	Seed      int64
	UserIndex int64
	Persona   string
	// Path is the node sequence the session traversed up to and including
	// this request. Producers attach it to FAILED observations only, as a
	// reslice of the session's precomputed walk (shared, never copied), so
	// the aggregator's per-observation footprint stays flat on healthy
	// traffic even though it retains every observation until Classify.
	Path []domain.ID
}

func (o RequestObservation) failed() bool {
	return o.StatusCode >= 400 || o.ErrorClass != ""
}

func (o RequestObservation) unavailable() bool {
	return o.StatusCode >= 500 || o.ErrorClass == "timeout" || o.ErrorClass == "transport"
}

// contractSignal reports whether o is a contract violation: a non-mutated
// happy-path request that returned a 5xx or failed an assertion. Shared by the
// obs-list classifier (classifyContract) and the Summary tally so the predicate
// cannot drift between the two paths.
func (o RequestObservation) contractSignal() bool {
	return !o.Mutated && (o.StatusCode >= 500 || o.ErrorClass == "assertion")
}

// mutationSignal reports whether o is a mutation finding: a mutated input that
// surfaced an error. Shared by the obs-list classifier (classifyMutation) and
// the Summary tally so the predicate cannot drift between the two paths.
func (o RequestObservation) mutationSignal() bool {
	return o.Mutated && o.failed()
}

// ClassifyConfig holds the thresholds used to turn observations into findings.
type ClassifyConfig struct {
	ErrorRateThreshold float64 // overall error rate above this -> threshold finding
	P95LatencyMs       float64 // overall p95 latency above this -> threshold finding (0 disables)
	AvailabilityRun    int     // this many consecutive failures on an API -> availability finding
}

// Default classification thresholds, applied whenever a run does not configure
// its own (see FindingConfig). They are the values every run classified with
// before the findings block existed, so changing one silently re-grades runs.
const (
	DefaultErrorRateThreshold = 0.2
	DefaultP95LatencyMs       = 0 // p95 gate disabled unless configured
	DefaultAvailabilityRun    = 5
)

// DefaultClassifyConfig returns the thresholds an unconfigured run classifies
// with: the long-standing 0.2 error rate, a 5-long availability streak, and
// the p95 gate disabled.
func DefaultClassifyConfig() ClassifyConfig {
	return ClassifyConfig{
		ErrorRateThreshold: DefaultErrorRateThreshold,
		P95LatencyMs:       DefaultP95LatencyMs,
		AvailabilityRun:    DefaultAvailabilityRun,
	}
}

// FindingConfig is the optional, operator-facing findings block of a run spec:
// the thresholds that classify a run's observations into findings. Every field
// is optional — a zero (or omitted) value falls back to the package default —
// so a spec without the block classifies exactly as before. The json tags are
// the wire contract shared by the RunSpec and the compact scenario file.
type FindingConfig struct {
	// ErrorRate is the overall error-rate threshold (0..1] above which a run
	// gets a threshold finding. Zero falls back to DefaultErrorRateThreshold.
	ErrorRate float64 `json:"errorRate,omitempty"`
	// P95LatencyMs gates the run's overall p95 latency: above this many
	// milliseconds a threshold finding is raised. Zero keeps the gate disabled
	// (the default).
	P95LatencyMs float64 `json:"p95LatencyMs,omitempty"`
	// AvailabilityStreak is how many consecutive failures on one API flag an
	// availability finding. Zero falls back to DefaultAvailabilityRun.
	AvailabilityStreak int `json:"availabilityStreak,omitempty"`
}

// Validate rejects thresholds that cannot classify anything sensibly: an error
// rate outside [0,1] or a negative latency/streak. Zero values are fine — they
// mean "use the default". It is nil-safe, like ClassifyConfig.
func (c *FindingConfig) Validate() error {
	if c == nil {
		return nil
	}
	if c.ErrorRate < 0 || c.ErrorRate > 1 {
		return fmt.Errorf("findings: errorRate %v out of range [0,1]", c.ErrorRate)
	}
	if c.P95LatencyMs < 0 {
		return fmt.Errorf("findings: p95LatencyMs %v must not be negative", c.P95LatencyMs)
	}
	if c.AvailabilityStreak < 0 {
		return fmt.Errorf("findings: availabilityStreak %d must not be negative", c.AvailabilityStreak)
	}
	return nil
}

// ClassifyConfig resolves the block into the thresholds the classifier runs
// with, filling unset (zero) fields from the package defaults. It is nil-safe:
// a spec that carries no findings block resolves to DefaultClassifyConfig.
func (c *FindingConfig) ClassifyConfig() ClassifyConfig {
	cfg := DefaultClassifyConfig()
	if c == nil {
		return cfg
	}
	if c.ErrorRate != 0 {
		cfg.ErrorRateThreshold = c.ErrorRate
	}
	if c.P95LatencyMs != 0 {
		cfg.P95LatencyMs = c.P95LatencyMs
	}
	if c.AvailabilityStreak != 0 {
		cfg.AvailabilityRun = c.AvailabilityStreak
	}
	return cfg
}

// Aggregator collects observations and classifies findings across four
// categories: threshold, contract, mutation and availability.
type Aggregator struct {
	mu  sync.Mutex
	obs []RequestObservation
}

// NewAggregator returns an empty aggregator.
func NewAggregator() *Aggregator { return &Aggregator{} }

// Add records one observation (safe for concurrent use).
func (a *Aggregator) Add(o RequestObservation) {
	a.mu.Lock()
	a.obs = append(a.obs, o)
	a.mu.Unlock()
}

// Classify returns the findings for runID under cfg. Findings are grouped per
// API per category so one bad endpoint yields one finding, not hundreds.
func (a *Aggregator) Classify(runID domain.ID, cfg ClassifyConfig) []domain.Finding {
	a.mu.Lock()
	obs := make([]RequestObservation, len(a.obs))
	copy(obs, a.obs)
	a.mu.Unlock()

	var findings []domain.Finding
	findings = append(findings, classifyMutation(runID, obs)...)
	findings = append(findings, classifyContract(runID, obs)...)
	findings = append(findings, classifyAvailability(runID, obs, cfg.AvailabilityRun)...)
	findings = append(findings, classifyThreshold(runID, obs, cfg)...)
	// Condense the retained observations into one bounded evidence bundle per
	// finding. This happens once, at classification time, so the per-request
	// hot path never builds evidence structures.
	attachEvidence(findings, obs, cfg)
	return findings
}

// hasEndpoint reports whether an observation can be attributed to a specific
// API. Observations with an empty APIID come from walk/setup failures (a step
// that never reached an endpoint), not endpoint behaviour, so the per-API
// classifiers skip them — this is also what keeps every per-API finding's
// EvidenceRef (the API id) non-empty, the invariant the run comparison relies
// on (see report.findingKey).
func (o RequestObservation) hasEndpoint() bool { return o.APIID != "" }

func classifyMutation(runID domain.ID, obs []RequestObservation) []domain.Finding {
	counts := map[domain.ID]int{}
	firstSeen := map[domain.ID]time.Time{}
	for _, o := range obs {
		if o.hasEndpoint() && o.mutationSignal() {
			counts[o.APIID]++
			if _, ok := firstSeen[o.APIID]; !ok {
				firstSeen[o.APIID] = o.TS
			}
		}
	}
	return findingsFromCounts(runID, domain.FindingMutation, domain.SeverityWarning, counts, firstSeen,
		"mutated input surfaced %d error(s) on %s")
}

func classifyContract(runID domain.ID, obs []RequestObservation) []domain.Finding {
	// A non-mutated request that gets a 5xx or fails an assertion is a contract
	// issue: the happy path produced an error a developer likely missed.
	counts := map[domain.ID]int{}
	firstSeen := map[domain.ID]time.Time{}
	for _, o := range obs {
		if o.hasEndpoint() && o.contractSignal() {
			counts[o.APIID]++
			if _, ok := firstSeen[o.APIID]; !ok {
				firstSeen[o.APIID] = o.TS
			}
		}
	}
	return findingsFromCounts(runID, domain.FindingContract, domain.SeverityCritical, counts, firstSeen,
		"%d contract violation(s) on %s (unexpected error on the happy path)")
}

// classifyAvailability flags APIs that suffered a long enough run of consecutive
// unavailable() results. The streak is evaluated in per-endpoint timestamp order
// (o.TS), so it no longer depends on the interleave in which concurrently-streamed
// results happened to be recorded: the same multiset of observations yields the
// same finding regardless of arrival order. Observations sharing an equal TS keep
// their insertion order (stable tie-break), which preserves the behaviour of the
// zero-TS / monotonic-TS test fixtures while making distinctly-timed events robust.
func classifyAvailability(runID domain.ID, obs []RequestObservation, run int) []domain.Finding {
	if run <= 0 {
		return nil
	}
	// Group by API in first-seen order, then sort each group by completion time.
	byAPI := map[domain.ID][]RequestObservation{}
	order := make([]domain.ID, 0)
	for _, o := range obs {
		if !o.hasEndpoint() {
			continue // walk/setup failure, not an endpoint's availability
		}
		if _, ok := byAPI[o.APIID]; !ok {
			order = append(order, o.APIID)
		}
		byAPI[o.APIID] = append(byAPI[o.APIID], o)
	}
	maxRun := map[domain.ID]int{}
	for _, api := range order {
		group := byAPI[api]
		sort.SliceStable(group, func(i, j int) bool { return group[i].TS.Before(group[j].TS) })
		cur := 0
		for _, o := range group {
			if o.unavailable() {
				cur++
				if cur > maxRun[api] {
					maxRun[api] = cur
				}
			} else {
				cur = 0
			}
		}
	}
	counts := map[domain.ID]int{}
	for api, m := range maxRun {
		if m >= run {
			counts[api] = m
		}
	}
	return findingsFromCounts(runID, domain.FindingAvailability, domain.SeverityCritical, counts, nil,
		"%d consecutive failures on %s (saturation or downtime)")
}

func classifyThreshold(runID domain.ID, obs []RequestObservation, cfg ClassifyConfig) []domain.Finding {
	var failed, total int
	latencies := make([]float64, 0, len(obs))
	for _, o := range obs {
		if o.Mutated {
			continue // mutation testing deliberately fails; not a threshold signal
		}
		total++
		if o.failed() {
			failed++
		}
		latencies = append(latencies, o.LatencyMs)
	}
	if total == 0 {
		return nil
	}
	rate := float64(failed) / float64(total)
	// Only sort + compute p95 when the p95 gate is enabled (preserves the
	// sort-only-when-needed behaviour); pass p95=0 when disabled.
	var p95 float64
	if cfg.P95LatencyMs > 0 {
		sort.Float64s(latencies)
		p95 = percentile(latencies, 0.95)
	}
	return thresholdFindings(runID, rate, p95, cfg)
}

// Evidence refs for findings that have no single API to point at. The run
// comparison keys findings by (category, evidenceRef), so each ref must be
// stable across runs, non-empty and distinct per issue: the two threshold
// findings carry their metric identity, and the Summary-derived coarse
// findings (which aggregate a whole run) are marked run-wide.
const (
	evidenceErrorRate  = "error-rate"
	evidenceP95Latency = "p95-latency"
	evidenceRunWide    = "run-wide"
)

// thresholdFindings builds the 0-2 threshold findings shared by the obs-list and
// Summary classifiers, so their messages and comparisons cannot drift.
func thresholdFindings(runID domain.ID, errorRate, p95 float64, cfg ClassifyConfig) []domain.Finding {
	var out []domain.Finding
	if cfg.ErrorRateThreshold > 0 && errorRate > cfg.ErrorRateThreshold {
		out = append(out, domain.Finding{RunID: runID, Category: domain.FindingThreshold, Severity: domain.SeverityWarning,
			EvidenceRef: evidenceErrorRate,
			Description: fmt.Sprintf("error rate %.2f exceeded threshold %.2f", errorRate, cfg.ErrorRateThreshold)})
	}
	if cfg.P95LatencyMs > 0 && p95 > cfg.P95LatencyMs {
		out = append(out, domain.Finding{RunID: runID, Category: domain.FindingThreshold, Severity: domain.SeverityWarning,
			EvidenceRef: evidenceP95Latency,
			Description: fmt.Sprintf("p95 latency %.1fms exceeded threshold %.1fms", p95, cfg.P95LatencyMs)})
	}
	return out
}

// --- finding evidence --------------------------------------------------------

// maxEvidenceSessions caps how many representative sessions are condensed into
// one finding's evidence bundle. The bundle exists to make a finding
// diagnosable, not to be a request log, so a handful chosen for spread beats
// hundreds in arrival order — and the cap is what keeps a finding's persisted
// size flat no matter how many requests failed behind it.
const maxEvidenceSessions = 5

// evidenceBucketLabels are the four fixed quarters of the observed run window
// the occurrence timing is bucketed into — coarse on purpose: enough to tell
// "early in ramp-up" from "late in soak" without persisting a timeline.
var evidenceBucketLabels = [4]string{"0-25%", "25-50%", "50-75%", "75-100%"}

// attachEvidence condenses the observation list into one bounded evidence
// bundle per finding: the observations matching each finding's identity become
// its status distribution, its timing buckets, and up to maxEvidenceSessions
// representative sessions. Findings whose observations yield nothing
// diagnosable (no session context, no status codes, no timestamps) keep a nil
// Evidence, so legacy-shaped findings remain byte-identical on the wire.
func attachEvidence(findings []domain.Finding, obs []RequestObservation, cfg ClassifyConfig) {
	if len(findings) == 0 {
		return
	}
	// The observed run window — first to last timestamped observation — is the
	// frame the timing buckets are relative to. The first observation lands
	// moments after the run starts, so it is a faithful stand-in for the run
	// start without threading the orchestrator's clock through Classify.
	winMin, winMax := observationWindow(obs)
	for i := range findings {
		cand := evidenceCandidates(findings[i], obs, cfg)
		if len(cand) == 0 {
			continue
		}
		ev := &domain.FindingEvidence{
			Sessions:     evidenceSessions(cand),
			TimeBuckets:  evidenceBuckets(cand, winMin, winMax),
			StatusCounts: evidenceStatusCounts(cand),
		}
		if ev.Sessions == nil && ev.TimeBuckets == nil && ev.StatusCounts == nil {
			continue // nothing diagnosable; keep the legacy shape
		}
		findings[i].Evidence = ev
	}
}

// evidenceCandidates returns the observations behind one finding, using the
// same predicates the classifiers counted with so the evidence can never
// disagree with the finding it backs. Threshold findings are run-wide: the
// error-rate bundle draws from every non-mutated failure, and the p95 bundle
// from every request over the gate — slowness, not failure, is its signal.
func evidenceCandidates(f domain.Finding, obs []RequestObservation, cfg ClassifyConfig) []RequestObservation {
	var match func(RequestObservation) bool
	switch f.Category {
	case domain.FindingMutation:
		api := domain.ID(f.EvidenceRef)
		match = func(o RequestObservation) bool { return o.hasEndpoint() && o.APIID == api && o.mutationSignal() }
	case domain.FindingContract:
		api := domain.ID(f.EvidenceRef)
		match = func(o RequestObservation) bool { return o.hasEndpoint() && o.APIID == api && o.contractSignal() }
	case domain.FindingAvailability:
		api := domain.ID(f.EvidenceRef)
		match = func(o RequestObservation) bool { return o.hasEndpoint() && o.APIID == api && o.unavailable() }
	case domain.FindingThreshold:
		switch f.EvidenceRef {
		case evidenceErrorRate:
			match = func(o RequestObservation) bool { return !o.Mutated && o.failed() }
		case evidenceP95Latency:
			match = func(o RequestObservation) bool { return !o.Mutated && o.LatencyMs > cfg.P95LatencyMs }
		default:
			return nil
		}
	default:
		return nil
	}
	var out []RequestObservation
	for _, o := range obs {
		if match(o) {
			out = append(out, o)
		}
	}
	return out
}

// evidenceSessions picks up to maxEvidenceSessions representative sessions
// from the candidates. Each session appears once (its earliest occurrence,
// insertion-stable on equal timestamps); when over the cap the pick is the
// earliest few — where an issue first surfaced — plus the slowest of the rest,
// which together cover both onset and worst case. Candidates without a session
// id (a producer that cannot attribute requests) yield no representatives.
func evidenceSessions(cand []RequestObservation) []domain.EvidenceSession {
	perSession := make(map[string]RequestObservation)
	order := make([]string, 0)
	for _, o := range cand {
		if o.SessionID == "" {
			continue
		}
		cur, ok := perSession[o.SessionID]
		if !ok {
			perSession[o.SessionID] = o
			order = append(order, o.SessionID)
		} else if o.TS.Before(cur.TS) {
			perSession[o.SessionID] = o
		}
	}
	if len(order) == 0 {
		return nil
	}
	reps := make([]RequestObservation, 0, len(order))
	for _, id := range order {
		reps = append(reps, perSession[id])
	}
	sort.SliceStable(reps, func(i, j int) bool { return reps[i].TS.Before(reps[j].TS) })
	if len(reps) > maxEvidenceSessions {
		earliest := (maxEvidenceSessions + 1) / 2
		head := reps[:earliest:earliest] // full slice expr: the append below must not clobber reps
		rest := reps[earliest:]
		sort.SliceStable(rest, func(i, j int) bool { return rest[i].LatencyMs > rest[j].LatencyMs })
		reps = append(head, rest[:maxEvidenceSessions-earliest]...)
	}
	out := make([]domain.EvidenceSession, len(reps))
	for i, o := range reps {
		out[i] = domain.EvidenceSession{
			SessionID:  o.SessionID,
			Seed:       o.Seed,
			UserIndex:  o.UserIndex,
			Persona:    o.Persona,
			Path:       o.Path,
			StatusCode: o.StatusCode,
			LatencyMs:  o.LatencyMs,
			ErrorClass: o.ErrorClass,
			TS:         o.TS,
		}
	}
	return out
}

// evidenceBuckets distributes the candidates' timestamps over four fixed
// quarters of the observed run window. Untimestamped candidates are skipped;
// when none carry a time (or the run window is unknown) no buckets are
// emitted. A zero-length window degenerates to everything in the first quarter.
func evidenceBuckets(cand []RequestObservation, winMin, winMax time.Time) []domain.EvidenceBucket {
	if winMin.IsZero() {
		return nil
	}
	window := winMax.Sub(winMin)
	var counts [4]int
	any := false
	for _, o := range cand {
		if o.TS.IsZero() {
			continue
		}
		q := 0
		if window > 0 {
			q = int(4 * o.TS.Sub(winMin) / window)
			if q > 3 {
				q = 3 // the window's last instant belongs to the final quarter
			}
		}
		counts[q]++
		any = true
	}
	if !any {
		return nil
	}
	out := make([]domain.EvidenceBucket, len(counts))
	for i, n := range counts {
		out[i] = domain.EvidenceBucket{Label: evidenceBucketLabels[i], Count: n}
	}
	return out
}

// evidenceStatusCounts tallies the candidates' HTTP status codes. Codes <= 0
// (transport-level failures) are skipped, matching Collector and Summary; a
// candidate set with no real codes yields nil rather than an empty map.
func evidenceStatusCounts(cand []RequestObservation) map[int]int {
	var counts map[int]int
	for _, o := range cand {
		if o.StatusCode <= 0 {
			continue
		}
		if counts == nil {
			counts = make(map[int]int)
		}
		counts[o.StatusCode]++
	}
	return counts
}

// observationWindow returns the earliest and latest non-zero timestamps across
// the observations; a zero winMin signals that nothing was timestamped.
func observationWindow(obs []RequestObservation) (winMin, winMax time.Time) {
	for _, o := range obs {
		if o.TS.IsZero() {
			continue
		}
		if winMin.IsZero() || o.TS.Before(winMin) {
			winMin = o.TS
		}
		if winMax.IsZero() || o.TS.After(winMax) {
			winMax = o.TS
		}
	}
	return winMin, winMax
}

// findingsFromCounts builds one finding per API present in counts, in stable
// API-id order, using a format string of (count, apiID). firstSeen supplies the
// per-API first-occurrence timestamp (may be nil to leave it zero).
func findingsFromCounts(runID domain.ID, cat domain.FindingCategory, sev domain.Severity, counts map[domain.ID]int, firstSeen map[domain.ID]time.Time, format string) []domain.Finding {
	if len(counts) == 0 {
		return nil
	}
	apis := make([]domain.ID, 0, len(counts))
	for api := range counts {
		apis = append(apis, api)
	}
	sort.Slice(apis, func(i, j int) bool { return apis[i] < apis[j] })

	out := make([]domain.Finding, 0, len(apis))
	for _, api := range apis {
		out = append(out, domain.Finding{
			RunID:       runID,
			Category:    cat,
			Severity:    sev,
			EvidenceRef: string(api),
			FirstSeen:   firstSeen[api],
			Description: fmt.Sprintf(format, counts[api], api),
			Count:       counts[api],
		})
	}
	return out
}
