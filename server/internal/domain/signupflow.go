package domain

import "fmt"

// SignupStep is one request in a declarative signup (or teardown) journey. It is
// transport-free: it carries the bare HTTP shape (method, rooted path, headers,
// body and response extractions) the way an APITemplate does, NOT the
// "METHOD /path" shorthand a config file authors — a layer above the domain (the
// orchestrator) compiles a flow's steps into a ScenarioGraph + APITemplates and
// runs them through the load runner. The body, path and headers may carry
// {{.userIndex}} / {{.subject}} placeholders the compiler renders per identity.
type SignupStep struct {
	// ID names the step (and the template the compiler derives from it); unique
	// within its flow.
	ID ID `json:"id"`
	// Method and Path are the request shape. Path is rooted ("/signup"); it and
	// the body may template the per-identity placeholders above.
	Method string `json:"method"`
	Path   string `json:"path"`
	// Headers are static request headers for the step.
	Headers map[string]string `json:"headers,omitempty"`
	// Body is the request payload template.
	Body string `json:"body,omitempty"`
	// Extract maps response fields onto captured variables for later steps and the
	// flow's Capture.
	Extract map[string]string `json:"extract,omitempty"`
	// DependsOn is the id of an earlier step this one requires; the compiler marks
	// that edge a dependency so a deviation never skips it.
	DependsOn ID `json:"dependsOn,omitempty"`
	// Weight is the probability of taking the edge to the next step (default 1).
	Weight float64 `json:"weight,omitempty"`
}

// Validate checks the step is callable: a non-empty id, method and rooted path.
func (s SignupStep) Validate() error {
	if s.ID == "" {
		return fmt.Errorf("signup step: id is required")
	}
	if s.Method == "" || s.Path == "" {
		return fmt.Errorf("signup step %q: method and path are required", s.ID)
	}
	return nil
}

// SignupCapture maps a signup flow's captured variables onto the minted
// credential: Token (the secret bearer token) and Subject (the non-sensitive
// principal id, optional but recommended so teardown and evidence can name the
// account). Both are optional: an empty Token means "auto-detect" — the runner
// resolves the token from the signup response (see load.DetectCredential) instead
// of an explicitly named capture.
type SignupCapture struct {
	// Token names the captured variable that becomes the credential's secret.
	// Optional: empty means auto-detect the token from the signup response.
	Token string `json:"token"`
	// Subject names the captured variable that becomes the non-sensitive subject.
	// Optional; when set it is also threaded into teardown as {{.subject}} so the
	// teardown journey can delete the exact account that was provisioned.
	Subject string `json:"subject,omitempty"`
}

// SignupFlow is the declarative bootstrap-signup journey a CredentialPool walks
// once per virtual user to provision a real account and capture its token, plus an
// optional teardown journey that deprovisions the account afterward. It is
// transport-free (raw steps + a capture mapping); the orchestrator compiles it to
// a graph + templates at provider-build time, so the domain imports no load or
// runspec.
//
// Teardown is a GATING SAFETY field, not a convenience: the rejection-lift path
// refuses a runnable bootstrap pool that declares no teardown (unless the operator
// passes --keep-accounts), so a load test never strands thousands of real accounts.
type SignupFlow struct {
	// Steps is the ordered signup journey (usually a single POST). The compiler
	// builds a graph from them in order.
	Steps []SignupStep `json:"steps"`
	// Start overrides the journey's entry node (defaults to the first step).
	Start ID `json:"start,omitempty"`
	// Capture names which captured variables become the credential's token/subject.
	Capture SignupCapture `json:"capture"`
	// Teardown is the optional deprovision journey, run once per provisioned
	// identity after the load finishes. Each step's {{.subject}} is the captured
	// subject of the account being torn down, so a "DELETE /accounts/{{.subject}}"
	// removes the exact account. Empty means no teardown — a gating-unsafe pool the
	// run path rejects unless --keep-accounts is set.
	Teardown []SignupStep `json:"teardown,omitempty"`
	// TeardownStart overrides the teardown journey's entry node (defaults to the
	// first teardown step).
	TeardownStart ID `json:"teardownStart,omitempty"`
}

// HasTeardown reports whether the flow declares a teardown journey. It is the
// gating-safety signal the rejection-lift path keys on: a bootstrap pool with no
// teardown is runnable only with --keep-accounts.
func (f *SignupFlow) HasTeardown() bool {
	return f != nil && len(f.Teardown) > 0
}

// Validate checks the flow is well-formed and can authenticate a run: a non-empty
// step list with unique ids and a present start node, and — when a teardown journey
// is declared — well-formed teardown steps with a present teardown start. The token
// capture is optional: an empty Capture.Token means the runner auto-detects the
// token from the signup response, so a flow without an explicit capture is valid. It
// validates shape only; compiling and running the flow is a concern of the layer
// above the domain.
func (f *SignupFlow) Validate() error {
	if f == nil {
		return fmt.Errorf("signup flow: required")
	}
	if len(f.Steps) == 0 {
		return fmt.Errorf("signup flow: at least one step is required")
	}
	if err := validateSignupSteps(f.Steps, f.Start, "signup"); err != nil {
		return err
	}
	if len(f.Teardown) > 0 {
		if err := validateSignupSteps(f.Teardown, f.TeardownStart, "teardown"); err != nil {
			return err
		}
	}
	return nil
}

// validateSignupSteps checks a step list has unique, well-formed steps and that
// the (optional) start node names one of them. kind labels the error ("signup" or
// "teardown").
func validateSignupSteps(steps []SignupStep, start ID, kind string) error {
	seen := make(map[ID]bool, len(steps))
	for _, st := range steps {
		if err := st.Validate(); err != nil {
			return fmt.Errorf("%s flow: %w", kind, err)
		}
		if seen[st.ID] {
			return fmt.Errorf("%s flow: duplicate step id %q", kind, st.ID)
		}
		seen[st.ID] = true
		if st.DependsOn != "" && !containsStep(steps, st.DependsOn) {
			return fmt.Errorf("%s flow: step %q dependsOn unknown step %q", kind, st.ID, st.DependsOn)
		}
	}
	if start != "" && !seen[start] {
		return fmt.Errorf("%s flow: start node %q is not a step", kind, start)
	}
	return nil
}

// containsStep reports whether steps declares a step with the given id.
func containsStep(steps []SignupStep, id ID) bool {
	for _, st := range steps {
		if st.ID == id {
			return true
		}
	}
	return false
}
