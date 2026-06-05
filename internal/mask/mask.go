// Package mask redacts sensitive values before they reach logs, reports or
// storage. It is deny-by-default: any field whose name looks sensitive (token,
// password, secret, email, phone, ...) is masked unless explicitly allowlisted,
// so a forgotten field leaks nothing (policy §5.3).
package mask

import (
	"encoding/json"
	"strings"
)

// defaultSensitive are substrings that mark a field name as sensitive.
var defaultSensitive = []string{
	"password", "passwd", "secret", "token", "authorization", "apikey",
	"api_key", "jwt", "session", "credential", "email", "phone", "ssn",
	"card", "cvv", "pin",
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
	for _, s := range defaultSensitive {
		if strings.Contains(f, s) {
			return true
		}
	}
	return false
}

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
	out, err := json.Marshal(masked)
	if err != nil {
		return data
	}
	return out
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
