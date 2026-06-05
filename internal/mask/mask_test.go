package mask

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDenyByDefault(t *testing.T) {
	m := New(Config{})
	// None of these are configured, but all look sensitive.
	for _, f := range []string{"password", "userToken", "Authorization", "email", "phoneNumber", "jwt", "sessionId"} {
		if !m.IsSensitive(f) {
			t.Errorf("%q should be masked by default", f)
		}
	}
	for _, f := range []string{"name", "quantity", "status", "id"} {
		if m.IsSensitive(f) {
			t.Errorf("%q should not be masked", f)
		}
	}
}

func TestAllowlistOverridesHeuristic(t *testing.T) {
	m := New(Config{Allow: []string{"email"}})
	if m.IsSensitive("email") {
		t.Error("allowlisted field must not be masked")
	}
	if !m.IsSensitive("password") {
		t.Error("non-allowlisted sensitive field must still be masked")
	}
}

func TestAlwaysMask(t *testing.T) {
	m := New(Config{Always: []string{"customField"}})
	if !m.IsSensitive("customField") {
		t.Error("explicitly-always field must be masked")
	}
}

func TestMaskJSONObject(t *testing.T) {
	m := New(Config{})
	in := []byte(`{"email":"a@b.com","name":"widget","token":"abc123"}`)
	out := m.MaskJSON(in)

	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("masked output invalid: %v", err)
	}
	if obj["email"] != "***" || obj["token"] != "***" {
		t.Errorf("sensitive fields not masked: %v", obj)
	}
	if obj["name"] != "widget" {
		t.Errorf("non-sensitive field altered: %v", obj["name"])
	}
	if strings.Contains(string(out), "a@b.com") || strings.Contains(string(out), "abc123") {
		t.Errorf("secret leaked in output: %s", out)
	}
}

func TestMaskJSONNested(t *testing.T) {
	m := New(Config{})
	in := []byte(`{"user":{"name":"x","password":"p"},"items":[{"cardNumber":"4111"}]}`)
	out := string(m.MaskJSON(in))
	if strings.Contains(out, `"p"`) || strings.Contains(out, "4111") {
		t.Errorf("nested secrets leaked: %s", out)
	}
	if !strings.Contains(out, `"name":"x"`) {
		t.Errorf("nested non-sensitive field lost: %s", out)
	}
}

func TestMaskValue(t *testing.T) {
	m := New(Config{Redaction: "[redacted]"})
	if got := m.MaskValue("authorization", "Bearer xyz"); got != "[redacted]" {
		t.Errorf("MaskValue sensitive = %q", got)
	}
	if got := m.MaskValue("city", "Seoul"); got != "Seoul" {
		t.Errorf("MaskValue non-sensitive = %q", got)
	}
}

func TestMaskJSONNonJSONPassthrough(t *testing.T) {
	m := New(Config{})
	in := []byte("plain log line with token=abc")
	if string(m.MaskJSON(in)) != string(in) {
		t.Error("non-JSON should pass through MaskJSON unchanged")
	}
}
