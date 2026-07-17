package importer

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// harAuthHeader walks the emitted flow and returns the Authorization header of the
// first step that carries one (or "" when none does).
func harAuthHeader(flow []scenariofile.Step) string {
	for _, st := range flow {
		for k, v := range st.Headers {
			if strings.EqualFold(k, "Authorization") {
				return v
			}
		}
	}
	return ""
}

// TestHARExtractsLoginStrategy covers a HAR with a login POST whose JSON response
// carries an access_token, followed by requests bearing Authorization: Bearer <that
// token>. FromHAR must emit a "login" strategy whose login flow IS that login call,
// leave the capture empty (E1 auto-detects), and rewrite the replayed steps so they
// carry Authorization: Bearer {{.token}} rather than the stale captured literal.
func TestHARExtractsLoginStrategy(t *testing.T) {
	const har = `{"log":{"entries":[
	    {"request":{"method":"POST","url":"http://app.test/login","postData":{"text":"{\"u\":\"a\"}"}},
	     "response":{"content":{"text":"{\"access_token\":\"abc\"}"}}},
	    {"request":{"method":"GET","url":"http://app.test/me","headers":[{"name":"Authorization","value":"Bearer abc"}]}},
	    {"request":{"method":"GET","url":"http://app.test/orders","headers":[{"name":"Authorization","value":"Bearer abc"}]}}
	  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if s.Auth == nil {
		t.Fatal("Auth is nil; want a login block extracted from the HAR")
	}
	if s.Auth.Strategy != "login" {
		t.Errorf("strategy = %q, want login", s.Auth.Strategy)
	}
	if s.Auth.Login == nil || len(s.Auth.Login.Flow) == 0 {
		t.Fatalf("login flow missing: %+v", s.Auth.Login)
	}
	if got := s.Auth.Login.Flow[0].Request; got != "POST /login" {
		t.Errorf("login flow request = %q, want POST /login", got)
	}
	if s.Auth.Login.Flow[0].Body != `{"u":"a"}` {
		t.Errorf("login flow body = %q, want the captured login body", s.Auth.Login.Flow[0].Body)
	}
	// Capture left empty so E1 auto-detects the token in the login response.
	if s.Auth.Login.Capture.Token != "" {
		t.Errorf("login capture token = %q, want empty (auto-detect)", s.Auth.Login.Capture.Token)
	}
	// No inline secret rides in the login block.
	if len(s.Auth.Users) != 0 {
		t.Errorf("login strategy should carry no inline users, got %+v", s.Auth.Users)
	}

	// The replayed steps that originally carried "Bearer abc" must now use {{.token}}.
	if h := harAuthHeader(s.Flow); h != "Bearer {{.token}}" {
		t.Errorf("replayed Authorization = %q, want Bearer {{.token}}", h)
	}
	for _, st := range s.Flow {
		for _, v := range st.Headers {
			if strings.Contains(v, "abc") {
				t.Errorf("step %q leaks the captured literal token: %q", st.ID, v)
			}
		}
	}
	// The login call itself must not appear in the replayed flow as an auth-bearing
	// step (it mints the token, it does not consume one). It may still be a normal
	// step, but with no Authorization header.
	if _, err := scenariofile.Expand(s); err != nil {
		t.Errorf("expand imported scenario: %v", err)
	}
}

// TestHARExtractsPoolStrategy covers a HAR with Authorization: Bearer xyz headers but
// NO discoverable login request. FromHAR must emit a "pool" strategy whose single
// inline entry's secret is the captured token, and rewrite the steps to {{.token}}.
func TestHARExtractsPoolStrategy(t *testing.T) {
	const har = `{"log":{"entries":[
	    {"request":{"method":"GET","url":"http://app.test/me","headers":[{"name":"Authorization","value":"Bearer xyz"}]}},
	    {"request":{"method":"GET","url":"http://app.test/orders","headers":[{"name":"Authorization","value":"Bearer xyz"}]}}
	  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if s.Auth == nil {
		t.Fatal("Auth is nil; want a pool block with the captured token")
	}
	if s.Auth.Strategy != "pool" {
		t.Errorf("strategy = %q, want pool", s.Auth.Strategy)
	}
	if s.Auth.Login != nil {
		t.Errorf("pool strategy must carry no login block, got %+v", s.Auth.Login)
	}
	if len(s.Auth.Users) != 1 {
		t.Fatalf("pool users = %d, want 1", len(s.Auth.Users))
	}
	if s.Auth.Users[0].Token != "xyz" {
		t.Errorf("pool token = %q, want the captured xyz", s.Auth.Users[0].Token)
	}
	// Steps rewritten to the pool token template.
	if h := harAuthHeader(s.Flow); h != "Bearer {{.token}}" {
		t.Errorf("replayed Authorization = %q, want Bearer {{.token}}", h)
	}
	for _, st := range s.Flow {
		for _, v := range st.Headers {
			if strings.Contains(v, "xyz") {
				t.Errorf("step %q leaks the captured literal token: %q", st.ID, v)
			}
		}
	}
	if _, err := scenariofile.Expand(s); err != nil {
		t.Errorf("expand imported scenario: %v", err)
	}
}

// TestHARNoAuthBackwardCompat is the control: a HAR with no Authorization header and
// no login request must import byte-for-byte as before — no auth block, same flow.
func TestHARNoAuthBackwardCompat(t *testing.T) {
	const har = `{"log":{"entries":[
	    {"request":{"method":"GET","url":"http://app.test/home"}},
	    {"request":{"method":"GET","url":"http://app.test/about"}}
	  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if s.Auth != nil {
		t.Errorf("Auth = %+v, want nil (no auth in the HAR)", s.Auth)
	}
	if len(s.Flow) != 2 {
		t.Fatalf("flow = %d steps, want 2", len(s.Flow))
	}
	for _, st := range s.Flow {
		if len(st.Headers) != 0 {
			t.Errorf("step %q grew headers %v; an unauthenticated HAR must import unchanged", st.ID, st.Headers)
		}
	}
}

// TestHARLoginByResponseToken covers the second login-detection path: the login
// entry's path does NOT match the keyword classifier ("/auth/exchange" carries no
// login keyword in its non-/token form), but its JSON response carries the captured
// token — so E1's DetectCredential identifies it as the minting call.
func TestHARLoginByResponseToken(t *testing.T) {
	const har = `{"log":{"entries":[
	    {"request":{"method":"POST","url":"http://app.test/auth/exchange","postData":{"text":"{\"code\":\"c\"}"}},
	     "response":{"content":{"text":"{\"jwt\":\"tok9\"}"}}},
	    {"request":{"method":"GET","url":"http://app.test/me","headers":[{"name":"Authorization","value":"Bearer tok9"}]}}
	  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if s.Auth == nil || s.Auth.Strategy != "login" {
		t.Fatalf("strategy = %+v, want a login block detected via the response token", s.Auth)
	}
	if got := s.Auth.Login.Flow[0].Request; got != "POST /auth/exchange" {
		t.Errorf("login flow request = %q, want POST /auth/exchange", got)
	}
	if h := harAuthHeader(s.Flow); h != "Bearer {{.token}}" {
		t.Errorf("replayed Authorization = %q, want Bearer {{.token}}", h)
	}
}

// TestHARLoginByPrecedingRequest covers the third login-detection path: no keyword
// match and no detectable response token, so the entry immediately preceding the
// first Authorization-bearing request is taken as the login call.
func TestHARLoginByPrecedingRequest(t *testing.T) {
	const har = `{"log":{"entries":[
	    {"request":{"method":"POST","url":"http://app.test/handshake","postData":{"text":"{\"k\":\"v\"}"}}},
	    {"request":{"method":"GET","url":"http://app.test/me","headers":[{"name":"Authorization","value":"Bearer pre1"}]}}
	  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if s.Auth == nil || s.Auth.Strategy != "login" {
		t.Fatalf("strategy = %+v, want a login block (preceding-request heuristic)", s.Auth)
	}
	if got := s.Auth.Login.Flow[0].Request; got != "POST /handshake" {
		t.Errorf("login flow request = %q, want POST /handshake", got)
	}
}

// TestHARNonBearerSchemePreserved confirms a non-Bearer Authorization scheme (e.g.
// "Token") is preserved in the rewritten header rather than forced to Bearer.
func TestHARNonBearerSchemePreserved(t *testing.T) {
	const har = `{"log":{"entries":[
	    {"request":{"method":"GET","url":"http://app.test/me","headers":[{"name":"Authorization","value":"Token raw42"}]}}
	  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if s.Auth == nil || s.Auth.Strategy != "pool" || s.Auth.Users[0].Token != "raw42" {
		t.Fatalf("auth = %+v, want a pool with token raw42", s.Auth)
	}
	if h := harAuthHeader(s.Flow); h != "Token {{.token}}" {
		t.Errorf("rewritten Authorization = %q, want Token {{.token}} (scheme preserved)", h)
	}
}

// TestHARMalformedEntryResilient confirms a malformed entry (a bad URL) does not break
// the whole import: the clean auth-bearing entry still yields a pool and a rewritten
// step.
func TestHARMalformedEntryResilient(t *testing.T) {
	const har = `{"log":{"entries":[
	    {"request":{"method":"GET","url":"://nonsense","headers":[{"name":"Authorization","value":"Bearer good"}]}},
	    {"request":{"method":"GET","url":"http://app.test/me","headers":[{"name":"Authorization","value":"Bearer good"}]}}
	  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if s.Auth == nil || s.Auth.Strategy != "pool" || s.Auth.Users[0].Token != "good" {
		t.Fatalf("auth = %+v, want a pool with token good despite the malformed entry", s.Auth)
	}
	if len(s.Flow) != 1 {
		t.Fatalf("flow = %d steps, want 1 (malformed entry dropped)", len(s.Flow))
	}
	if h := harAuthHeader(s.Flow); h != "Bearer {{.token}}" {
		t.Errorf("rewritten Authorization = %q, want Bearer {{.token}}", h)
	}
}

// TestHARExtractsCookieToken covers a session/auth cookie rather than an
// Authorization header: the token rides in a Cookie request header whose name
// matches the credential-cookie set (session/token/jwt/auth/sid). FromHAR must
// capture that cookie value as the pool token (no login request here).
func TestHARExtractsCookieToken(t *testing.T) {
	const har = `{"log":{"entries":[
	    {"request":{"method":"GET","url":"http://app.test/me","headers":[{"name":"Cookie","value":"theme=dark; session=cookietok"}]}},
	    {"request":{"method":"GET","url":"http://app.test/orders","headers":[{"name":"Cookie","value":"theme=dark; session=cookietok"}]}}
	  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if s.Auth == nil {
		t.Fatal("Auth is nil; want a pool block with the captured cookie token")
	}
	if s.Auth.Strategy != "pool" {
		t.Errorf("strategy = %q, want pool", s.Auth.Strategy)
	}
	if len(s.Auth.Users) != 1 || s.Auth.Users[0].Token != "cookietok" {
		t.Fatalf("pool users = %+v, want one entry with token cookietok", s.Auth.Users)
	}
}
