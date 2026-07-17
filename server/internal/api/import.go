package api

import (
	"fmt"
	"io"
	"net/http"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/runspec"
)

// ImportFunc converts an uploaded API description (OpenAPI or HAR) in the given
// format ("auto", "openapi", or "har") into a runnable RunSpec. It is injected
// via WithImporter so the api package stays free of the importer/scenariofile
// packages (both import api, so importing them here would be a cycle).
type ImportFunc func(data []byte, format string) (RunSpec, error)

// WithImporter wires the scenario importer used by POST /import. Without it the
// import endpoint reports 501 Not Implemented.
func WithImporter(fn ImportFunc) Option {
	return func(s *Server) { s.importFn = fn }
}

// ImportStatsFunc is the stats-aware variant of ImportFunc: alongside the
// RunSpec it returns optional coverage stats describing what the importer kept
// and dropped. The access-log learner has real coverage to report; spec
// conversions (OpenAPI/HAR) may return nil, which omits the field from the
// response. Wired with WithImporterStats.
type ImportStatsFunc func(data []byte, format string) (RunSpec, *ImportStats, error)

// WithImporterStats wires a stats-aware importer for POST /import. When set it
// takes precedence over WithImporter, and the response carries an optional
// "stats" object so the UI can show an import coverage report ("what did the
// learned miniature drop?"). Backward compatible: stats is omitted when nil,
// so old clients see the exact pre-stats response shape.
func WithImporterStats(fn ImportStatsFunc) Option {
	return func(s *Server) { s.importStatsFn = fn }
}

// ImportStats mirrors importer.AccessLogStats onto the wire: what the learner
// kept and dropped, so a capped or noisy import is reported instead of silently
// passing as full coverage. The api package cannot name the importer's type
// directly (injection keeps the packages decoupled), so the injected
// ImportStatsFunc maps the fields across.
type ImportStats struct {
	// Format is the resolved access-log format profile (importer.Format*
	// constant): the explicit hint when one was given, else what detection
	// recognized. Omitted when the importer reports none.
	Format string `json:"format,omitempty"`
	// Requests is the number of usable log records (parsed, non-asset).
	Requests int `json:"requests"`
	// Skipped counts lines that did not parse or were filtered out (assets,
	// unsupported methods).
	Skipped int `json:"skipped"`
	// Sessions is the number of per-client visits after gap splitting.
	Sessions int `json:"sessions"`
	// Clients is the number of distinct IP + user-agent identities.
	Clients int `json:"clients"`
	// DroppedEndpoints counts endpoints beyond the node cap that were folded
	// out of the graph (their transitions bridge across them).
	DroppedEndpoints int `json:"droppedEndpoints"`
	// SkippedSamples optionally carries a few example dropped lines so the UI
	// can show why coverage is partial. Every field is best-effort: an importer
	// that tracks no diagnostics simply leaves it empty.
	SkippedSamples []ImportSkippedSample `json:"skippedSamples,omitempty"`
}

// ImportSkippedSample is one example line the importer dropped, small enough to
// render in a diagnostic table.
type ImportSkippedSample struct {
	// Line is the 1-based line number in the uploaded document; 0 when unknown.
	Line int `json:"line,omitempty"`
	// Text is the raw line, possibly truncated by the importer.
	Text string `json:"text,omitempty"`
	// Reason says why the line was dropped (unparsable, asset, method, …).
	Reason string `json:"reason,omitempty"`
}

// importResult is the form-relevant slice of a converted RunSpec the UI needs to
// prefill an experiment: the behavior graph, the per-node API templates, the
// entry node, and a suggested step bound — plus, when the importer reports it,
// the coverage stats behind the import, and (P7) the auth the importer derived so
// the UI can prefill credentials instead of discarding them.
//
// FROZEN CONTRACT (the web codes against exactly these field names): the auth
// fields are all omitempty, so an unauthenticated import returns the exact
// pre-P7 shape. None of them ever carries a real secret — domain.Credential.Secret
// is json:"-", so a captured pool secret (E3 HAR) is dropped here too (AD-011);
// login/signup flows carry only REPLACE_ME placeholders, never a minted token.
type importResult struct {
	Graph     any          `json:"graph"`
	Templates any          `json:"templates"`
	Start     string       `json:"start"`
	MaxSteps  int          `json:"maxSteps"`
	Stats     *ImportStats `json:"stats,omitempty"`
	// CredentialPool is the Expanded spec's pool (strategy + entries + loginFlowId +
	// loginScope + signupFlow + keepAccounts). Its entries' secrets are dropped by
	// domain.Credential's json:"-" tag, so a captured pool secret never crosses to
	// the browser. Present when the import derived auth.
	CredentialPool *domain.CredentialPool `json:"credentialPool,omitempty"`
	// LoginFlow is the standalone login flow (graph/templates/start/tokenVar/
	// subjectVar) the UI prefills when the pool's strategy is "login". It carries
	// only the form body's REPLACE_ME placeholders, never a token.
	LoginFlow *runspec.LoginFlowSpec `json:"loginFlow,omitempty"`
	// SuggestedSignup is the signup flow the importer derived from a register/signup
	// operation, offered as a "create test accounts" suggestion independent of the
	// primary pool. Present when a register op was detected.
	SuggestedSignup *domain.SignupFlow `json:"suggestedSignup,omitempty"`
}

// importMaxBytes bounds an uploaded API description. It is larger than a normal
// API request (maxRequestBytes) because real HAR captures routinely run to a few
// megabytes; the importer only reads it, so the headroom is safe.
const importMaxBytes = 32 << 20 // 32 MiB

// handleImport converts an uploaded OpenAPI or HAR description into a scenario
// (graph + templates + start) so the UI can prefill an experiment from an API
// spec instead of hand-writing JSON. The request body is the raw description and
// ?format selects the parser (default "auto", detected from content). Conversion
// is delegated to the injected importer; only the form-relevant fields return.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if s.importFn == nil && s.importStatsFn == nil {
		writeErr(w, http.StatusNotImplemented, fmt.Errorf("api: import is not configured"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, importMaxBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, fmt.Errorf("api: description exceeds the %d MiB import limit", importMaxBytes>>20))
		return
	}
	if len(data) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("api: empty request body"))
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "auto"
	}
	// Prefer the stats-aware importer; the legacy one remains as the fallback so
	// existing wiring keeps working (it just never attaches a coverage report).
	var spec RunSpec
	var stats *ImportStats
	if s.importStatsFn != nil {
		spec, stats, err = s.importStatsFn(data, format)
	} else {
		spec, err = s.importFn(data, format)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("import: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, importResult{
		Graph:     spec.Graph,
		Templates: spec.Templates,
		Start:     string(spec.Start),
		MaxSteps:  spec.MaxSteps,
		Stats:     stats,
		// Surface the auth the importer derived (P7). These come straight off the
		// Expanded spec: the pool (login/pool/bootstrap), the standalone login flow
		// for a login pool, and the advisory signup suggestion. All omitempty, so an
		// unauthenticated import returns the pre-P7 shape. No secret crosses here —
		// the pool's entry secrets are json:"-" and the flows carry only placeholders.
		CredentialPool:  spec.CredentialPool,
		LoginFlow:       spec.LoginFlow,
		SuggestedSignup: spec.SuggestedSignup,
	})
}
