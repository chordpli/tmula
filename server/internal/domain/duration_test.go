package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestMintSpecDurationWireForms pins that a mint spec accepts its TTL/Leeway as a
// human duration STRING (what the web console posts and a hand-authored spec writes)
// AND as a nanosecond number (what a Go-marshaled ShardSpec produces), and marshals
// back as a string. Before flexDuration, a string 400'd every web-authored mint run
// and the mint "Test token" preflight with "cannot unmarshal string into ... TTL".
func TestMintSpecDurationWireForms(t *testing.T) {
	t.Run("string ttl (web/hand-authored)", func(t *testing.T) {
		var m MintSpec
		if err := json.Unmarshal([]byte(`{"alg":"HS256","secretEncoding":"raw","ttl":"1h0m0s","leeway":"30s","key":{"env":"K"}}`), &m); err != nil {
			t.Fatalf("decode string ttl: %v", err)
		}
		if m.TTL != time.Hour {
			t.Fatalf("TTL: want 1h, got %v", m.TTL)
		}
		if m.Leeway != 30*time.Second {
			t.Fatalf("Leeway: want 30s, got %v", m.Leeway)
		}
	})

	t.Run("number ttl (Go-marshaled ShardSpec)", func(t *testing.T) {
		var m MintSpec
		if err := json.Unmarshal([]byte(`{"alg":"HS256","ttl":3600000000000,"key":{"env":"K"}}`), &m); err != nil {
			t.Fatalf("decode number ttl: %v", err)
		}
		if m.TTL != time.Hour {
			t.Fatalf("TTL: want 1h, got %v", m.TTL)
		}
	})

	t.Run("marshals ttl as a string", func(t *testing.T) {
		out, err := json.Marshal(MintSpec{Alg: MintHS256, TTL: time.Hour, Key: &CredentialSourceRef{Env: "K"}})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(out), `"ttl":"1h0m0s"`) {
			t.Fatalf("want a string ttl in %s", out)
		}
	})
}

// TestExecSpecTimeoutWireForms pins the same string-or-number acceptance for the exec
// timeout, which the web console likewise posts as a Go-duration string.
func TestExecSpecTimeoutWireForms(t *testing.T) {
	var e ExecSpec
	if err := json.Unmarshal([]byte(`{"command":["/bin/token"],"timeout":"45s"}`), &e); err != nil {
		t.Fatalf("decode string timeout: %v", err)
	}
	if e.Timeout != 45*time.Second {
		t.Fatalf("Timeout: want 45s, got %v", e.Timeout)
	}
	var e2 ExecSpec
	if err := json.Unmarshal([]byte(`{"command":["/bin/token"],"timeout":45000000000}`), &e2); err != nil {
		t.Fatalf("decode number timeout: %v", err)
	}
	if e2.Timeout != 45*time.Second {
		t.Fatalf("Timeout: want 45s, got %v", e2.Timeout)
	}
	out, err := json.Marshal(ExecSpec{Command: []string{"/bin/token"}, Timeout: 45 * time.Second})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"timeout":"45s"`) {
		t.Fatalf("want a string timeout in %s", out)
	}
}
