package load

import (
	"testing"
	"time"
)

// TestDetectRefreshOAuth2Body picks the refresh_token and expires_in out of a
// realistic OAuth2 token response — the shape a login endpoint returns alongside
// the access_token.
func TestDetectRefreshOAuth2Body(t *testing.T) {
	body := []byte(`{"access_token":"abc","refresh_token":"r-123","token_type":"bearer","expires_in":3600}`)
	refresh, expiresIn := DetectRefresh(body)
	if refresh != "r-123" {
		t.Errorf("refresh = %q, want %q", refresh, "r-123")
	}
	if expiresIn != 3600*time.Second {
		t.Errorf("expiresIn = %v, want %v", expiresIn, 3600*time.Second)
	}
}

// TestDetectRefreshNested resolves a refresh token nested in a data envelope, the
// same convention DetectCredential uses for the access token.
func TestDetectRefreshNested(t *testing.T) {
	body := []byte(`{"data":{"refresh_token":"deep","expires_in":120}}`)
	refresh, expiresIn := DetectRefresh(body)
	if refresh != "deep" {
		t.Errorf("nested refresh = %q, want %q", refresh, "deep")
	}
	if expiresIn != 120*time.Second {
		t.Errorf("nested expiresIn = %v, want %v", expiresIn, 120*time.Second)
	}
}

// TestDetectRefreshCamelCase matches refreshToken regardless of case, mirroring
// the access-token key list.
func TestDetectRefreshCamelCase(t *testing.T) {
	body := []byte(`{"refreshToken":"r-camel","expiresIn":60}`)
	refresh, expiresIn := DetectRefresh(body)
	if refresh != "r-camel" {
		t.Errorf("camel refresh = %q, want %q", refresh, "r-camel")
	}
	if expiresIn != 60*time.Second {
		t.Errorf("camel expiresIn = %v, want %v", expiresIn, 60*time.Second)
	}
}

// TestDetectRefreshAbsent is the no-behavior-change guard at the detector layer: a
// response with neither field yields the zero refresh and zero duration, so a
// caller folds in nothing.
func TestDetectRefreshAbsent(t *testing.T) {
	body := []byte(`{"access_token":"abc","username":"alice"}`)
	refresh, expiresIn := DetectRefresh(body)
	if refresh != "" {
		t.Errorf("refresh = %q, want empty (absent)", refresh)
	}
	if expiresIn != 0 {
		t.Errorf("expiresIn = %v, want 0 (absent)", expiresIn)
	}
}

// TestDetectRefreshInvalidJSON degrades gracefully on an unparseable body rather
// than panicking — same contract as DetectCredential.
func TestDetectRefreshInvalidJSON(t *testing.T) {
	refresh, expiresIn := DetectRefresh([]byte("not json"))
	if refresh != "" || expiresIn != 0 {
		t.Errorf("invalid json: refresh=%q expiresIn=%v, want empty/0", refresh, expiresIn)
	}
}

// TestDetectRefreshExpiresInFractional accepts a fractional expires_in (some
// servers emit a JSON number with a decimal) and rounds to the nearest second-
// resolution duration without truncating to zero.
func TestDetectRefreshExpiresInFractional(t *testing.T) {
	body := []byte(`{"refresh_token":"r","expires_in":1.5}`)
	_, expiresIn := DetectRefresh(body)
	if expiresIn != 1500*time.Millisecond {
		t.Errorf("fractional expiresIn = %v, want %v", expiresIn, 1500*time.Millisecond)
	}
}

// TestDetectCredentialOAuth2Body detects the token from a standard OAuth2 token
// response — the most common shape a login endpoint returns.
func TestDetectCredentialOAuth2Body(t *testing.T) {
	body := []byte(`{"access_token":"abc","token_type":"bearer","expires_in":3600}`)
	token, _ := DetectCredential(body, nil)
	if token != "abc" {
		t.Errorf("token = %q, want %q", token, "abc")
	}
}

// TestDetectCredentialNested walks nested objects (up to ~depth 3) so a token
// wrapped in a data/result envelope is still found.
func TestDetectCredentialNested(t *testing.T) {
	body := []byte(`{"data":{"token":"x"}}`)
	token, _ := DetectCredential(body, nil)
	if token != "x" {
		t.Errorf("nested token = %q, want %q", token, "x")
	}
}

// TestDetectCredentialNestedAccessToken finds a nested access_token via the
// ranked dotted-path list (data.access_token).
func TestDetectCredentialNestedAccessToken(t *testing.T) {
	body := []byte(`{"data":{"access_token":"deep"}}`)
	token, _ := DetectCredential(body, nil)
	if token != "deep" {
		t.Errorf("nested access_token = %q, want %q", token, "deep")
	}
}

// TestDetectCredentialRanking proves the ranked key list wins: access_token
// outranks a plain token even when both are present at the top level.
func TestDetectCredentialRanking(t *testing.T) {
	body := []byte(`{"token":"low","access_token":"high"}`)
	token, _ := DetectCredential(body, nil)
	if token != "high" {
		t.Errorf("ranked token = %q, want access_token to win (%q)", token, "high")
	}
}

// TestDetectCredentialSetCookie falls back to a session cookie when the body
// carries no recognizable token field.
func TestDetectCredentialSetCookie(t *testing.T) {
	body := []byte(`{"ok":true}`)
	token, _ := DetectCredential(body, []string{"session=xyz; Path=/; HttpOnly"})
	if token != "xyz" {
		t.Errorf("cookie token = %q, want %q", token, "xyz")
	}
}

// TestDetectCredentialBodyBeatsCookie keeps the body token authoritative over a
// cookie when both are present (the body is the explicit credential surface).
func TestDetectCredentialBodyBeatsCookie(t *testing.T) {
	body := []byte(`{"access_token":"frombody"}`)
	token, _ := DetectCredential(body, []string{"session=fromcookie; Path=/"})
	if token != "frombody" {
		t.Errorf("token = %q, want the body token to win (%q)", token, "frombody")
	}
}

// TestDetectCredentialCookieRanking prefers an auth-shaped cookie name over an
// unrelated one and ignores cookies that are not credential-shaped.
func TestDetectCredentialCookieRanking(t *testing.T) {
	body := []byte(`{}`)
	token, _ := DetectCredential(body, []string{"ab_test=42; Path=/", "jwt=tok; Path=/; Secure"})
	if token != "tok" {
		t.Errorf("cookie token = %q, want the jwt cookie (%q)", token, "tok")
	}
}

// TestDetectCredentialSubject detects the principal id from a ranked subject-key
// list (username here).
func TestDetectCredentialSubject(t *testing.T) {
	body := []byte(`{"access_token":"abc","username":"alice"}`)
	token, subject := DetectCredential(body, nil)
	if token != "abc" {
		t.Errorf("token = %q, want %q", token, "abc")
	}
	if subject != "alice" {
		t.Errorf("subject = %q, want %q", subject, "alice")
	}
}

// TestDetectCredentialSubjectNested resolves a dotted subject path (user.id).
func TestDetectCredentialSubjectNested(t *testing.T) {
	body := []byte(`{"token":"t","user":{"id":"u-1"}}`)
	_, subject := DetectCredential(body, nil)
	if subject != "u-1" {
		t.Errorf("nested subject = %q, want %q", subject, "u-1")
	}
}

// TestDetectCredentialCaseInsensitive matches keys regardless of case, so a
// camel/Pascal/snake variant is all detected the same way.
func TestDetectCredentialCaseInsensitive(t *testing.T) {
	body := []byte(`{"AccessToken":"camel"}`)
	token, _ := DetectCredential(body, nil)
	if token != "camel" {
		t.Errorf("case-insensitive token = %q, want %q", token, "camel")
	}
}

// TestDetectCredentialNothingFound returns empty strings when neither a token nor
// a subject is present — the caller decides whether that is an error.
func TestDetectCredentialNothingFound(t *testing.T) {
	body := []byte(`{"message":"hello","count":3}`)
	token, subject := DetectCredential(body, nil)
	if token != "" {
		t.Errorf("token = %q, want empty", token)
	}
	if subject != "" {
		t.Errorf("subject = %q, want empty", subject)
	}
}

// TestDetectCredentialEmptyValueSkipped skips an empty-string token value and
// keeps searching for a non-empty one.
func TestDetectCredentialEmptyValueSkipped(t *testing.T) {
	body := []byte(`{"access_token":"","token":"real"}`)
	token, _ := DetectCredential(body, nil)
	if token != "real" {
		t.Errorf("token = %q, want the non-empty value (%q)", token, "real")
	}
}

// TestDetectCredentialInvalidJSON degrades gracefully: an unparseable body with
// no cookie yields empty strings rather than panicking.
func TestDetectCredentialInvalidJSON(t *testing.T) {
	token, subject := DetectCredential([]byte("not json"), nil)
	if token != "" || subject != "" {
		t.Errorf("invalid json: token=%q subject=%q, want both empty", token, subject)
	}
}

// TestDetectCredentialInvalidJSONCookie still reads a cookie token when the body
// is unparseable.
func TestDetectCredentialInvalidJSONCookie(t *testing.T) {
	token, _ := DetectCredential([]byte("<html>"), []string{"auth=cookietok; Path=/"})
	if token != "cookietok" {
		t.Errorf("token = %q, want the cookie token (%q)", token, "cookietok")
	}
}
