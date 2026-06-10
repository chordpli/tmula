package mask

import (
	"strings"
	"testing"
)

// TestNoOverMasking is the regression for the substring over-masking bug:
// short ambiguous tokens (pin/card/...) must only match whole tokens.
func TestNoOverMasking(t *testing.T) {
	m := New(Config{})
	mustNot := []string{"shipping_address", "shoppingCart", "discard_changes", "spinner", "mapping", "monkey", "standard"}
	for _, f := range mustNot {
		if m.IsSensitive(f) {
			t.Errorf("%q should NOT be masked (false positive)", f)
		}
	}
	must := []string{"user_pin", "cardNumber", "card", "ssn", "otp_code", "accountIban", "PIN"}
	for _, f := range must {
		if !m.IsSensitive(f) {
			t.Errorf("%q SHOULD be masked", f)
		}
	}
}

// TestPassphraseMasked is the regression for the under-masking leak.
func TestPassphraseMasked(t *testing.T) {
	m := New(Config{})
	for _, f := range []string{"passphrase", "private_key", "signature", "cookie"} {
		if !m.IsSensitive(f) {
			t.Errorf("%q must be masked", f)
		}
	}
}

func TestNoHTMLEscape(t *testing.T) {
	out := string(New(Config{}).MaskJSON([]byte(`{"url":"https://x/?a=1&b=2","tag":"<b>"}`)))
	if strings.Contains(out, "\\u0026") || strings.Contains(out, "\\u003c") {
		t.Errorf("non-sensitive value was HTML-escaped: %s", out)
	}
	if !strings.Contains(out, "a=1&b=2") || !strings.Contains(out, "<b>") {
		t.Errorf("characters mangled: %s", out)
	}
}
