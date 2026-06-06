// Package report renders run results as standalone, self-contained HTML: a
// single page with inline styles and no external assets, suitable for handing
// to a non-operator (a PM) or archiving. It also diffs two runs side by side.
//
// It depends only on the domain and obs types so the api package can call into
// it without an import cycle; report never imports api.
package report
