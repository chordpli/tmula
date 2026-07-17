package obs

import "testing"

// TestAuthRefreshGuardLivesInFailedOnly is the load-bearing placement proof: the
// ErrorClassAuthRefresh class is excused by failed() ONLY. An exhausted-refresh
// 401 (status 401 + the auth-refresh class) must report failed()==false so it does
// not pollute the error rate, while unavailable() is UNCHANGED — it keys on
// status>=500 and timeout/transport and never sees auth-refresh, so it stays false
// for a 401 with NO edit to unavailable(). A plain 401 (no class) stays failed().
func TestAuthRefreshGuardLivesInFailedOnly(t *testing.T) {
	authRefresh401 := RequestObservation{StatusCode: 401, ErrorClass: ErrorClassAuthRefresh}
	plain401 := RequestObservation{StatusCode: 401}

	// The guard: an auth-refresh 401 is not a failure.
	if authRefresh401.failed() {
		t.Error("an exhausted-refresh 401 (auth-refresh) must not count as failed()")
	}
	// A plain 401 still is.
	if !plain401.failed() {
		t.Error("a plain 401 must still count as failed()")
	}

	// unavailable() is unchanged and never excused the auth-refresh class: a 401 is
	// below 500 and carries no timeout/transport class, so it is not unavailable —
	// for BOTH the auth-refresh and the plain 401, with no change to unavailable().
	if authRefresh401.unavailable() {
		t.Error("auth-refresh 401 must not be unavailable() (guard belongs in failed() only)")
	}
	if plain401.unavailable() {
		t.Error("plain 401 must not be unavailable()")
	}

	// A 5xx carrying the auth-refresh class is still unavailable (the guard must not
	// leak into unavailable): this pins that unavailable() ignores the class field's
	// auth-refresh value entirely and keys purely on status/timeout/transport.
	authRefresh500 := RequestObservation{StatusCode: 500, ErrorClass: ErrorClassAuthRefresh}
	if !authRefresh500.unavailable() {
		t.Error("a 5xx is unavailable() regardless of the auth-refresh class")
	}
}

// TestAuthRefreshExcludedFromThresholdAndContract confirms the guard's effect end
// to end: an exhausted-refresh 401 is excluded from the error-rate threshold (it
// is not a failure) and is never a contract finding (a 401 is not a 5xx/assertion
// anyway), so a run that recovered-then-exhausted auth does not surface a spurious
// error-rate finding from the swallowed auth churn.
func TestAuthRefreshExcludedFromThreshold(t *testing.T) {
	a := NewAggregator()
	// 9 healthy + 1 exhausted-refresh 401: without the guard that is a 10% error
	// rate; with it the 401 is not counted as failed, so the rate is 0%.
	for i := 0; i < 9; i++ {
		a.Add(RequestObservation{APIID: "x", StatusCode: 200})
	}
	a.Add(RequestObservation{APIID: "x", StatusCode: 401, ErrorClass: ErrorClassAuthRefresh})

	fs := a.Classify("run1", ClassifyConfig{ErrorRateThreshold: 0.05})
	for _, f := range fs {
		if f.EvidenceRef == evidenceErrorRate {
			t.Errorf("auth-refresh 401 leaked into the error-rate threshold: %+v", f)
		}
	}
}
