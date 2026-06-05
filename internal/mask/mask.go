// Package mask redacts sensitive values before they reach logs, reports or
// storage. It is deny-by-default: any field whose name looks sensitive (token,
// password, secret, email, phone, ...) is masked unless explicitly allowlisted,
// so a forgotten field leaks nothing (policy §5.3).
package mask

import (
	"bytes"
	"encoding/json"
	"strings"
)

// substringSensitive are unambiguous substrings: if the field name contains
// one, it is masked (e.g. "userToken", "billing_email").
var substringSensitive = []string{
	"password", "passwd", "passphrase", "secret", "token", "authorization",
	"apikey", "api_key", "jwt", "session", "credential", "private",
	"signature", "cookie", "bearer", "email", "phone",
}

// tokenSensitive are short/ambiguous names matched only as whole tokens (split
// on non-alphanumeric and camelCase), so "pin"/"card" mask "user_pin"/"cardNo"
// but NOT "shipping_address" or "shopping_cart".
var tokenSensitive = []string{
	"pin", "card", "cvv", "ssn", "otp", "key", "salt", "iban", "dob",
}

// Config configures a Masker.
type Config struct {
	// Always are extra field names (case-insensitive) to always mask.
	Always []string
	// Allow are field names to never mask, even if they look sensitive.
	Allow []string
	// Redaction is the replacement value (defaults to "***").
	Redaction string
}

// Masker redacts sensitive fields in JSON payloads and single values.
type Masker struct {
	always    map[string]bool
	allow     map[string]bool
	redaction string
}

// New builds a Masker from cfg.
func New(cfg Config) *Masker {
	m := &Masker{
		always:    toSet(cfg.Always),
		allow:     toSet(cfg.Allow),
		redaction: cfg.Redaction,
	}
	if m.redaction == "" {
		m.redaction = "***"
	}
	return m
}

func toSet(xs []string) map[string]bool {
	s := make(map[string]bool, len(xs))
	for _, x := range xs {
		s[strings.ToLower(strings.TrimSpace(x))] = true
	}
	return s
}

// IsSensitive reports whether a field name should be masked.
func (m *Masker) IsSensitive(field string) bool {
	f := strings.ToLower(field)
	if m.allow[f] {
		return false
	}
	if m.always[f] {
		return true
	}
	for _, s := range substringSensitive {
		if strings.Contains(f, s) {
			return true
		}
	}
	if len(tokenSensitive) > 0 {
		tokens := tokenize(field)
		for _, t := range tokens {
			for _, s := range tokenSensitive {
				if t == s {
					return true
				}
			}
		}
	}
	return false
}

// tokenize splits a field name into lowercased tokens on non-alphanumeric
// separators and camelCase boundaries: "userPin" -> ["user","pin"],
// "shipping_address" -> ["shipping","address"].
func tokenize(field string) []string {
	var tokens []string
	start := -1
	flush := func(end int) {
		if start >= 0 {
			tokens = append(tokens, strings.ToLower(field[start:end]))
			start = -1
		}
	}
	for i := 0; i < len(field); i++ {
		c := field[i]
		if !isAlnum(c) {
			flush(i)
			continue
		}
		if start >= 0 && isUpper(c) && !isUpper(field[i-1]) {
			flush(i) // camelCase boundary
		}
		if start < 0 {
			start = i
		}
	}
	flush(len(field))
	return tokens
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }

// MaskValue returns the value to use for a field: redacted if the field is
// sensitive, otherwise the original value.
func (m *Masker) MaskValue(field, value string) string {
	if m.IsSensitive(field) {
		return m.redaction
	}
	return value
}

// MaskJSON walks a JSON document and redacts the values of sensitive fields at
// any depth. A payload that is not valid JSON is returned unchanged (callers
// should mask non-JSON via MaskValue).
func (m *Masker) MaskJSON(data []byte) []byte {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return data
	}
	masked := m.walk(v)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // don't turn <,>,& in non-sensitive values into \u00xx
	if err := enc.Encode(masked); err != nil {
		return data
	}
	return bytes.TrimRight(buf.Bytes(), "\n") // Encoder appends a trailing newline
}

func (m *Masker) walk(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if m.IsSensitive(k) {
				t[k] = m.redaction
				continue
			}
			t[k] = m.walk(child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = m.walk(child)
		}
		return t
	default:
		return v
	}
}
