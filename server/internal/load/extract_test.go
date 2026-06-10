package load

import "testing"

func TestExtractVariablesReadsJSONPaths(t *testing.T) {
	got, err := ExtractVariables([]byte(`{
		"items": [{"id": "p7"}],
		"cart": {"id": 42},
		"ok": true,
		"tags": ["sale", "new"]
	}`), map[string]string{
		"productId": "items.0.id",
		"cartId":    "$.cart.id",
		"ok":        "ok",
		"tags":      "tags",
	})
	if err != nil {
		t.Fatalf("ExtractVariables: %v", err)
	}
	want := map[string]string{
		"productId": "p7",
		"cartId":    "42",
		"ok":        "true",
		"tags":      `["sale","new"]`,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("extracted %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestExtractVariablesMissingPathErrors(t *testing.T) {
	if _, err := ExtractVariables([]byte(`{"item":{}}`), map[string]string{"productId": "item.id"}); err == nil {
		t.Fatal("expected an error for a missing extraction path")
	}
}
