package api

import (
	"fmt"
	"io"
	"net/http"
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

// importResult is the form-relevant slice of a converted RunSpec the UI needs to
// prefill an experiment: the behavior graph, the per-node API templates, the
// entry node, and a suggested step bound.
type importResult struct {
	Graph     any    `json:"graph"`
	Templates any    `json:"templates"`
	Start     string `json:"start"`
	MaxSteps  int    `json:"maxSteps"`
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
	if s.importFn == nil {
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
	spec, err := s.importFn(data, format)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("import: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, importResult{
		Graph:     spec.Graph,
		Templates: spec.Templates,
		Start:     string(spec.Start),
		MaxSteps:  spec.MaxSteps,
	})
}
