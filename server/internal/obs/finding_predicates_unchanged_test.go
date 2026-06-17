package obs

import "testing"

// TestObsPredicatesUnchangedByAuthNote is the guard for TASK 2's hard constraint:
// the "auth likely expired" run note is a report-build-time observability signal
// derived from aggregated status counts ONLY, and must NOT change how an
// observation is classified. The four classification predicates —
// failed(), unavailable(), contractSignal(), mutationSignal() — therefore keep
// their exact pre-note truth tables. This test pins those tables so any edit to a
// predicate (in particular, any attempt to make a 401/403 special there) fails
// loudly. It is a behavioral byte-for-byte audit: the predicates are unchanged.
func TestObsPredicatesUnchangedByAuthNote(t *testing.T) {
	cases := []struct {
		name                                                string
		o                                                   RequestObservation
		failed, unavailable, contractSignal, mutationSignal bool
	}{
		// A 401/403 is a plain client-side rejection: failed() (>=400) but NOT
		// unavailable (below 500, no timeout/transport) and NOT a contract or
		// mutation signal. The note must not have made the predicates treat it
		// specially — this is the central pin for TASK 2.
		{"plain 401", RequestObservation{StatusCode: 401}, true, false, false, false},
		{"plain 403", RequestObservation{StatusCode: 403}, true, false, false, false},
		// An exhausted-refresh 401 is excused by failed() ONLY (the P-prior guard),
		// and is untouched by the note work.
		{"auth-refresh 401", RequestObservation{StatusCode: 401, ErrorClass: ErrorClassAuthRefresh}, false, false, false, false},
		// A clean 200 trips nothing.
		{"clean 200", RequestObservation{StatusCode: 200}, false, false, false, false},
		// A 5xx is failed + unavailable + a contract signal (non-mutated happy path).
		{"500", RequestObservation{StatusCode: 500}, true, true, true, false},
		// A timeout class is failed + unavailable, no contract (no 5xx, no assertion).
		{"timeout", RequestObservation{ErrorClass: "timeout"}, true, true, false, false},
		// A transport error is failed + unavailable.
		{"transport", RequestObservation{ErrorClass: "transport"}, true, true, false, false},
		// An assertion failure on the happy path is failed + a contract signal.
		{"assertion", RequestObservation{ErrorClass: "assertion"}, true, false, true, false},
		// A mutated input that errors is a mutation signal (and failed), but a
		// mutated request is never a contract signal.
		{"mutated 500", RequestObservation{StatusCode: 500, Mutated: true}, true, true, false, true},
		{"mutated 401", RequestObservation{StatusCode: 401, Mutated: true}, true, false, false, true},
		// A mutated success is not a mutation signal.
		{"mutated 200", RequestObservation{StatusCode: 200, Mutated: true}, false, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.o.failed(); got != c.failed {
				t.Errorf("failed() = %v, want %v", got, c.failed)
			}
			if got := c.o.unavailable(); got != c.unavailable {
				t.Errorf("unavailable() = %v, want %v", got, c.unavailable)
			}
			if got := c.o.contractSignal(); got != c.contractSignal {
				t.Errorf("contractSignal() = %v, want %v", got, c.contractSignal)
			}
			if got := c.o.mutationSignal(); got != c.mutationSignal {
				t.Errorf("mutationSignal() = %v, want %v", got, c.mutationSignal)
			}
		})
	}
}
