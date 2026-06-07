package api

import (
	"fmt"
	"html"
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
	rep, ok := s.reportFor(id)
	if !ok {
		writeHTMLError(w, http.StatusNotFound, fmt.Sprintf("Run %q is not available.", id))
		return
	}
	out, err := report.HTML(reportData(rep))
	if err != nil {
		writeHTMLError(w, http.StatusInternalServerError, "The report could not be rendered.")
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
		writeHTMLError(w, http.StatusBadRequest, "Both run ids (a and b) are required.")
		return
	}
	if aID == bID {
		writeHTMLError(w, http.StatusBadRequest, "Pick two different runs to compare.")
		return
	}

	repA, aok := s.reportFor(aID)
	repB, bok := s.reportFor(bID)
	if !aok {
		writeHTMLError(w, http.StatusNotFound, fmt.Sprintf("Run %q is not available.", aID))
		return
	}
	if !bok {
		writeHTMLError(w, http.StatusNotFound, fmt.Sprintf("Run %q is not available.", bID))
		return
	}

	out, err := report.CompareHTML(reportData(repA), reportData(repB))
	if err != nil {
		writeHTMLError(w, http.StatusInternalServerError, "The comparison could not be rendered.")
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

// writeHTMLError renders a small standalone HTML error page, so a stale or bad
// report/compare link opened in a browser explains itself rather than returning
// a raw JSON error. msg may carry a user-controlled run id, so it is escaped.
func writeHTMLError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>tmula</title>`+
		`<body style="font-family:system-ui,sans-serif;max-width:560px;margin:4rem auto;padding:0 1rem;color:#333">`+
		`<h1 style="font-size:1.25rem">Report unavailable</h1><p>%s</p>`+
		`<p style="color:#888;font-size:.9rem">Runs are held in memory, so a link can stop working after the engine restarts.</p>`+
		`</body>`, html.EscapeString(msg))
}
