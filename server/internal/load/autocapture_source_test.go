package load

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestDetectCredentialSourceLabelsOrigin pins the machine-readable source label the
// auto-capture log and the preflight endpoint both read: a body token reports its JSON
// key, a cookie token reports the Set-Cookie name.
func TestDetectCredentialSourceLabelsOrigin(t *testing.T) {
	tok, _, src := DetectCredentialSource([]byte(`{"access_token":"eyJreal","user":"alice"}`), nil)
	if tok != "eyJreal" {
		t.Fatalf("token = %q, want eyJreal", tok)
	}
	if src != "body:access_token" {
		t.Errorf("source = %q, want body:access_token", src)
	}

	// No body token: a credential-shaped Set-Cookie is the fallback source.
	tok, _, src = DetectCredentialSource([]byte(`{"ok":true}`), []string{"session=abc123; Path=/; HttpOnly"})
	if tok != "abc123" {
		t.Fatalf("cookie token = %q, want abc123", tok)
	}
	if src != "cookie:session" {
		t.Errorf("source = %q, want cookie:session", src)
	}

	// A non-JSON body with a cookie still resolves the cookie source.
	tok, _, src = DetectCredentialSource([]byte(`<html>error</html>`), []string{"authToken=zzz; Path=/"})
	if tok != "zzz" || src != "cookie:authToken" {
		t.Errorf("non-JSON cookie: token=%q source=%q, want zzz / cookie:authToken", tok, src)
	}
}

// TestLogAutoCaptureSourceNamesSourceNotValue proves the auto-capture log names WHERE
// the token came from and NEVER the token value, for both the body-key and cookie cases,
// and that the cookie line carries the Cookie-header replay hint.
func TestLogAutoCaptureSourceNamesSourceNotValue(t *testing.T) {
	restore := slog.Default()
	defer slog.SetDefault(restore)

	logOf := func(source string) string {
		var buf bytes.Buffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
		LogAutoCaptureSource(source)
		return buf.String()
	}

	// Body key: the message names the response-body key, never the secret.
	body := logOf("body:access_token")
	if !strings.Contains(body, `auth: token auto-detected from response body key`) || !strings.Contains(body, "access_token") {
		t.Errorf("body log should name the body key, got %q", body)
	}

	// Cookie: the message names the cookie AND how to replay it as a Cookie header.
	cookie := logOf("cookie:session")
	if !strings.Contains(cookie, `Set-Cookie`) || !strings.Contains(cookie, "session") {
		t.Errorf("cookie log should name the Set-Cookie source, got %q", cookie)
	}
	if !strings.Contains(cookie, "Cookie: session={{.token}}") {
		t.Errorf("cookie log should carry the replay hint, got %q", cookie)
	}

	// The value must never be logged: the source label is a name, not the token.
	valueBearing := logOf("body:access_token")
	if strings.Contains(valueBearing, "eyJreal") {
		t.Errorf("the log must never contain a token value, got %q", valueBearing)
	}

	// An empty source (an explicit capture won) logs nothing.
	if got := logOf(""); got != "" {
		t.Errorf("an empty source should log nothing, got %q", got)
	}
}
