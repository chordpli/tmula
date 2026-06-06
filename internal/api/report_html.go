package api

import (
	"fmt"
	"net/http"

	"github.com/chordpli/tmula/internal/domain"
	"github.com/chordpli/tmula/internal/report"
)

// reportData maps a control-plane Report onto the report package's input so the
// renderer never has to import api (which would close an import cycle).
func reportData(rep Report) report.Data {
	return report.Data{
		Run:      rep.Run,
		Stats:    rep.Stats,
		Findings: rep.Findings,
		Workers:  rep.Workers,
	}
}

// getReportHTML serves a run's report as a standalone HTML page (the operator
// view; unlike the shared report it is not PII-masked). It is reached at
// GET /api/runs/{id}/report.html.
func (s *Server) getReportHTML(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("id"))
	s.mu.Lock()
	rs, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("run %q not found", id))
		return
	}
	out, err := report.HTML(reportData(rs.report()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeHTML(w, out)
}

// compareRuns serves a side-by-side HTML comparison of two runs, selected by
// GET /api/runs/compare?a=<id>&b=<id>. It is a 400 when a or b is missing or the
// two are equal, and a 404 when either run is unknown.
func (s *Server) compareRuns(w http.ResponseWriter, r *http.Request) {
	aID := domain.ID(r.URL.Query().Get("a"))
	bID := domain.ID(r.URL.Query().Get("b"))
	if aID == "" || bID == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("both a and b run ids are required"))
		return
	}
	if aID == bID {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("a and b must be different runs"))
		return
	}

	s.mu.Lock()
	ra, aok := s.runs[aID]
	rb, bok := s.runs[bID]
	s.mu.Unlock()
	if !aok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("run %q not found", aID))
		return
	}
	if !bok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("run %q not found", bID))
		return
	}

	out, err := report.CompareHTML(reportData(ra.report()), reportData(rb.report()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeHTML(w, out)
}

// writeHTML writes a rendered page with the HTML content type. The bytes are
// already escaped by html/template, so they are written verbatim.
func writeHTML(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
