package load

import "testing"

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
