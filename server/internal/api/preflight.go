package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"github.com/chordpli/tmula/server/internal/domain"
	"github.com/chordpli/tmula/server/internal/load"
	"github.com/chordpli/tmula/server/internal/safety"
)

// PreflightResult is the "configure auth → test it immediately" answer: whether a single
// credential could be acquired for user index 0, and enough non-secret context to see how
// (the strategy, the HTTP status of the auth call, WHERE the token was detected, and a
// short, non-reversible prefix of it). On failure OK is false and Reason is an enriched,
// actionable explanation. The full token NEVER appears — only TokenPrefix (6 chars + "…").
type PreflightResult struct {
	OK       bool   `json:"ok"`
	Strategy string `json:"strategy"`
	// HTTPStatus is the status of the auth call (the login/signup response). Omitted for
	// strategies that make no HTTP call to acquire (pool/mint) or run a command (exec).
	HTTPStatus int `json:"httpStatus,omitempty"`
	// TokenSource names WHERE the token came from: "body:<key>", "cookie:<name>",
	// "header:<name>", or the literal "pool"/"mint"/"exec"/"signup". It is a name, never
	// a value.
	TokenSource string `json:"tokenSource,omitempty"`
	// TokenPrefix is the first 6 characters of the token plus "…" — enough to confirm a
	// token was produced (and eyeball a JWT header) without leaking the secret. Empty when
	// no token was acquired.
	TokenPrefix string `json:"tokenPrefix,omitempty"`
	// Subject is the non-sensitive principal id the credential carries, when known.
	Subject string `json:"subject,omitempty"`
	// Reason is the enriched failure explanation when OK is false.
	Reason string `json:"reason,omitempty"`
}

// authPreflight implements POST /auth/preflight: decode a run spec (the same shape as
// POST /experiments), build the credential provider from its auth config, and acquire ONE
// credential (user index 0) through the SAME safety guard a run uses — a preflight must
// never escape the target allowlist. It returns 200 with {ok:true,...} on success and 200
// with {ok:false,reason:...} on an auth FAILURE (a 401, a blocked host), so a caller reads
// the outcome from the body, not the HTTP status. It returns 400 for an unusable spec and
// 403 for a gated exec strategy (mirroring StartRun).
func (s *Server) authPreflight(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var spec RunSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode: %w", err))
		return
	}
	if spec.CredentialPool == nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("preflight needs a credentialPool to test"))
		return
	}
	// The target env must be well-formed to build the guard the auth call goes through.
	if err := spec.TargetEnv.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The exec strategy runs an arbitrary local command; gate the preflight exactly like
	// StartRun — a scenario merely declaring exec must not execute anything without the
	// operator opt-in.
	if spec.CredentialPool.Strategy == domain.CredExec && !s.allowExec {
		writeErr(w, http.StatusForbidden, fmt.Errorf("the %q credential strategy runs an arbitrary local command and is disabled by default; enable it with --allow-exec (server WithAllowExec) to preflight it", domain.CredExec))
		return
	}
	// Full safety policy, rebuilt from the spec exactly as StartRun builds it: a prod-lock
	// or an allowlist that excludes the target is a safety refusal (403), never a silent
	// escape.
	guard, err := safety.NewGuardForEnv(spec.TargetEnv, nil, false)
	if err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	if err := guard.AllowHost(spec.TargetEnv.BaseURL); err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	writeJSON(w, http.StatusOK, s.doPreflight(r.Context(), spec, guard))
}

// doPreflight acquires one credential for user index 0 by strategy and shapes the result.
// It never returns an error: an acquisition failure is reported as {ok:false, reason} so
// the caller always gets a 200 body describing the outcome.
func (s *Server) doPreflight(ctx context.Context, spec RunSpec, guard *safety.Guard) PreflightResult {
	strategy := string(spec.CredentialPool.Strategy)
	switch spec.CredentialPool.Strategy {
	case domain.CredLogin:
		return s.preflightLogin(ctx, spec, guard)
	case domain.CredBootstrapSignup:
		return s.preflightBootstrap(ctx, spec, guard)
	case domain.CredMint:
		return preflightFromProvider(ctx, spec, strategy, "mint")
	case domain.CredExec:
		return preflightFromProvider(ctx, spec, strategy, "exec")
	case domain.CredPool:
		return preflightFromProvider(ctx, spec, strategy, "pool")
	default:
		return PreflightResult{OK: false, Strategy: strategy, Reason: fmt.Sprintf("unknown credential strategy %q", strategy)}
	}
}

// preflightFromProvider handles the strategies that acquire a credential WITHOUT a login
// flow — pool (parse entry 0), mint (resolve the key and sign one token), and exec (run
// the command once, already gated above). tokenSource is the literal strategy name because
// there is no HTTP response to detect a key/cookie from.
func preflightFromProvider(ctx context.Context, spec RunSpec, strategy, source string) PreflightResult {
	provider, err := spec.CredentialProvider()
	if err != nil {
		return PreflightResult{OK: false, Strategy: strategy, Reason: err.Error()}
	}
	if provider == nil {
		return PreflightResult{OK: false, Strategy: strategy, Reason: "no credential provider was built for this strategy"}
	}
	cred, err := provider.Acquire(ctx, 0)
	if err != nil {
		return PreflightResult{OK: false, Strategy: strategy, Reason: err.Error()}
	}
	return PreflightResult{
		OK:          true,
		Strategy:    strategy,
		TokenSource: source,
		TokenPrefix: tokenPrefix(cred.Secret),
		Subject:     cred.Subject,
	}
}

// preflightLogin runs the login flow ONCE for user index 0 through the guarded runner
// (the same allowlist/rate cap a run enforces), then reports the token source, the HTTP
// status and the token prefix. An explicit capture path is authoritative; otherwise the
// token/source are auto-detected. A non-2xx login is reported as ok:false with the status
// and an enriched, actionable reason.
func (s *Server) preflightLogin(ctx context.Context, spec RunSpec, guard *safety.Guard) PreflightResult {
	const strategy = "login"
	flow, runner, err := s.compileLoginFlow(spec, guard)
	if err != nil {
		return PreflightResult{OK: false, Strategy: strategy, Reason: err.Error()}
	}
	nodeTmpl, err := runner.ResolveNodeTemplates(flow.Graph)
	if err != nil {
		return PreflightResult{OK: false, Strategy: strategy, Reason: fmt.Sprintf("compile login flow: %v", err)}
	}
	maxSteps := flow.MaxSteps
	if maxSteps <= 0 {
		maxSteps = loginMaxStepsDefault
	}
	// Seed the login-input row 0 exactly as NewLoginTokenFunc does, so the preflight logs
	// in as the same account VU 0 would.
	var row domain.Credential
	if len(flow.Entries) > 0 {
		row = flow.Entries[0]
	}
	user := load.VirtualUser{
		ID: "preflight-0",
		Vars: map[string]string{
			"userIndex": "0",
			"username":  row.Subject,
			"password":  row.Secret,
			"secret":    row.Secret,
		},
		Cred: domain.Credential{Subject: row.Subject, Secret: row.Secret},
	}
	captured, final, err := runner.RunOnceCapture(ctx, flow.Graph, nodeTmpl, flow.Start, maxSteps, user, spec.Seed)
	if err != nil {
		return PreflightResult{
			OK:         false,
			Strategy:   strategy,
			HTTPStatus: statusFromError(err),
			Reason:     enrichLoginFailureReason(err),
		}
	}

	// Explicit capture wins; otherwise auto-detect the token, subject and source.
	autoToken, autoSubject, autoSource := load.DetectCredentialSource(final.Body, final.SetCookie)
	token, source := autoToken, autoSource
	if flow.TokenVar != "" {
		token, source = captured[flow.TokenVar], "body:"+flow.TokenVar
	}
	subject := autoSubject
	if flow.SubjectVar != "" {
		subject = captured[flow.SubjectVar]
	} else if subject == "" {
		subject = row.Subject
	}
	if token == "" {
		return PreflightResult{
			OK:         false,
			Strategy:   strategy,
			HTTPStatus: http.StatusOK,
			Reason:     "the login succeeded but no token was found in the response; set an explicit capture path (auth.login.capture.token)",
		}
	}
	return PreflightResult{
		OK:          true,
		Strategy:    strategy,
		HTTPStatus:  http.StatusOK,
		TokenSource: source,
		TokenPrefix: tokenPrefix(token),
		Subject:     subject,
	}
}

// preflightBootstrap provisions ONE real account (index 0) and then tears it down, so a
// bootstrap preflight leaves no probe account behind. It REFUSES (politely, ok:false) a
// pool with no teardown flow rather than leaking an account. The provider is built with
// teardown wired; Teardown runs on a fresh context so it deprovisions even if ctx is done.
func (s *Server) preflightBootstrap(ctx context.Context, spec RunSpec, guard *safety.Guard) PreflightResult {
	const strategy = "bootstrap-signup"
	pool := spec.CredentialPool
	if pool.SignupFlow == nil || !pool.SignupFlow.HasTeardown() {
		return PreflightResult{
			OK:       false,
			Strategy: strategy,
			Reason:   "bootstrap-signup preflight provisions one real account and needs a teardown flow to remove it afterward; declare auth.signup.teardown before preflighting (or preflight a login/pool/mint strategy)",
		}
	}
	boot, err := s.bootstrapAuthFor(spec, guard)
	if err != nil {
		return PreflightResult{OK: false, Strategy: strategy, Reason: err.Error()}
	}
	// Always tear the probe account down, on a FRESH context (ctx may be done by then), so
	// the preflight never strands the account it created.
	defer func() {
		tctx, cancel := context.WithTimeout(context.Background(), teardownBaseTimeout)
		defer cancel()
		_ = boot.provider.Teardown(tctx)
	}()

	cred, err := boot.provider.Acquire(ctx, 0)
	if err != nil {
		return PreflightResult{
			OK:         false,
			Strategy:   strategy,
			HTTPStatus: statusFromError(err),
			Reason:     enrichLoginFailureReason(err),
		}
	}
	source := "signup"
	if v := pool.SignupFlow.Capture.Token; v != "" {
		source = "body:" + v
	}
	return PreflightResult{
		OK:          true,
		Strategy:    strategy,
		HTTPStatus:  http.StatusOK,
		TokenSource: source,
		TokenPrefix: tokenPrefix(cred.Secret),
		Subject:     cred.Subject,
	}
}

// tokenPrefix returns a short, non-reversible preview of a secret: its first 6 characters
// plus "…". It NEVER returns the whole token, so a preflight response cannot leak the
// secret. A token of 6 or fewer characters (a misconfiguration) is elided entirely so even
// a tiny token is not echoed whole.
func tokenPrefix(token string) string {
	if token == "" {
		return ""
	}
	const show = 6
	// Operate on runes so a multibyte token is not sliced mid-character.
	runes := []rune(token)
	if len(runes) <= show {
		return "…"
	}
	return string(runes[:show]) + "…"
}

// loginFailRE captures the step name and HTTP status out of a flow failure
// ("... runOnce node \"login\" returned status 401 ..."), so the preflight can both
// report the status and phrase an actionable reason. statusRE is the status-only variant
// used when only the code is needed.
var (
	loginFailRE = regexp.MustCompile(`runOnce node "([^"]+)" returned status (\d{3})`)
	statusRE    = regexp.MustCompile(`returned status (\d{3})`)
)

// statusFromError pulls the HTTP status out of a flow failure error, or 0 when none is
// present (a transport error, a blocked host).
func statusFromError(err error) int {
	if err == nil {
		return 0
	}
	if m := statusRE.FindStringSubmatch(err.Error()); m != nil {
		if code, e := strconv.Atoi(m[1]); e == nil {
			return code
		}
	}
	return 0
}

// enrichLoginFailureReason turns a raw flow failure into an actionable preflight reason:
// a status-bearing failure ("returned status 401") is rewritten as a login-flavored
// message with a "check the login URL and credentials" hint; anything else passes through.
// The underlying errors carry no secret, so nothing sensitive is echoed.
func enrichLoginFailureReason(err error) string {
	msg := err.Error()
	if m := loginFailRE.FindStringSubmatch(msg); m != nil {
		return fmt.Sprintf("login flow step %q returned status %s — check the login URL and credentials", m[1], m[2])
	}
	return msg
}
