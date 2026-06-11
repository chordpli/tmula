// Package gate decides whether a run's findings should fail a CI job relative
// to a baseline run. It buckets findings into new / resolved / persisting /
// suppressed using the report package's (category, evidenceRef) identity, and
// applies an expiring known-issues list so accepted problems do not block
// unrelated changes — but come back loudly once their expiry passes.
//
// It depends only on domain and report so the CLI can call into it without
// pulling in the control plane.
package gate
