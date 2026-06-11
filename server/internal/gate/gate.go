package gate

import (
	"fmt"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/report"
)

// KnownIssue is one suppression entry in a known-issues YAML file. Every field
// is required: category and evidenceRef pin which finding it matches (exact
// match, same identity the diff uses), reason justifies the suppression, and
// expires forces a re-triage date so no issue is silenced forever.
type KnownIssue struct {
	Category    string `json:"category"`
	EvidenceRef string `json:"evidenceRef"`
	Reason      string `json:"reason"`
	// Expires is the last (UTC) day the suppression is valid, as YYYY-MM-DD.
	Expires string `json:"expires"`
	// ExpiresAt is Expires parsed to midnight UTC; ParseKnownIssues fills it. A
	// zero value reads as long expired, so a hand-built entry that skipped
	// parsing fails safe (no suppression) instead of suppressing forever.
	ExpiresAt time.Time `json:"-"`
}

// expired reports whether the entry no longer suppresses at now. The expiry
// day itself is still valid — a date-granular field must not flip mid-day
// depending on when in the day CI happens to run — so the boundary is the
// midnight (UTC) after ExpiresAt.
func (k KnownIssue) expired(now time.Time) bool {
	return !now.Before(k.ExpiresAt.AddDate(0, 0, 1))
}

// matches reports whether the entry suppresses the finding: an exact match on
// the (category, evidenceRef) identity. No globbing — a suppression should be
// as narrow as the finding it accepts.
func (k KnownIssue) matches(f domain.Finding) bool {
	return k.Category == string(f.Category) && k.EvidenceRef == f.EvidenceRef
}

// ParseKnownIssues parses a known-issues YAML document (a list of entries) and
// validates every entry. It fails loud on any incomplete entry: a suppression
// missing its identity would never match, missing its reason is unjustified,
// and missing its expiry would silence an issue forever.
func ParseKnownIssues(data []byte) ([]KnownIssue, error) {
	var issues []KnownIssue
	if err := yaml.UnmarshalStrict(data, &issues); err != nil {
		return nil, fmt.Errorf("parse known issues: %w", err)
	}
	for i := range issues {
		ki := &issues[i]
		switch {
		case ki.Category == "":
			return nil, fmt.Errorf("known issue [%d]: category is required", i)
		case ki.EvidenceRef == "":
			return nil, fmt.Errorf("known issue [%d]: evidenceRef is required", i)
		case ki.Reason == "":
			return nil, fmt.Errorf("known issue [%d]: reason is required", i)
		case ki.Expires == "":
			return nil, fmt.Errorf("known issue [%d]: expires is required (YYYY-MM-DD)", i)
		}
		t, err := time.ParseInLocation("2006-01-02", ki.Expires, time.UTC)
		if err != nil {
			return nil, fmt.Errorf("known issue [%d]: expires %q is not YYYY-MM-DD", i, ki.Expires)
		}
		ki.ExpiresAt = t
	}
	return issues, nil
}

// Suppressed pairs a suppressed finding with the known issue that matched it,
// so output can show what was silenced and why (reason, expiry).
type Suppressed struct {
	Finding domain.Finding
	Issue   KnownIssue
}

// Result is the gate's verdict on one run against a baseline. Only New fails
// the gate; the other buckets exist so the output stays honest about what was
// already broken (Persisting), what got fixed (Resolved), what was accepted
// (Suppressed) and which suppressions need re-triage (Expired).
type Result struct {
	New        []domain.Finding
	Resolved   []domain.Finding
	Persisting []domain.Finding
	Suppressed []Suppressed
	// Expired lists every expired known issue, matched or not, so dead entries
	// are flagged for cleanup rather than rotting in the file.
	Expired []KnownIssue
}

// Evaluate classifies the current run's findings against the baseline run's
// findings and the known-issues list, as of now. Suppression applies only to
// the New bucket: persisting findings never fail the baseline gate, so
// reclassifying them would only hide state, and an expired entry never
// suppresses — its finding stays New and the entry is reported in Expired.
func Evaluate(baseline, current []domain.Finding, known []KnownIssue, now time.Time) Result {
	d := report.DiffAgainstBaseline(baseline, current)
	res := Result{Resolved: d.Resolved, Persisting: d.Persisting}

	var active []KnownIssue
	for _, ki := range known {
		if ki.expired(now) {
			res.Expired = append(res.Expired, ki)
			continue
		}
		active = append(active, ki)
	}

	for _, f := range d.New {
		if ki, ok := match(active, f); ok {
			res.Suppressed = append(res.Suppressed, Suppressed{Finding: f, Issue: ki})
			continue
		}
		res.New = append(res.New, f)
	}
	return res
}

// match returns the first active known issue matching the finding.
func match(active []KnownIssue, f domain.Finding) (KnownIssue, bool) {
	for _, ki := range active {
		if ki.matches(f) {
			return ki, true
		}
	}
	return KnownIssue{}, false
}
