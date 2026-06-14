package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOpenAPISpecIsValidAndRelative locks the contract that lets tmula scaffold
// straight from the running URL: GET /openapi.json must be a valid OpenAPI 3
// doc whose single server is RELATIVE ("/"). A relative server makes the
// scaffolder target whatever host it fetched the spec from, so no port stays
// hard-coded in the spec. It must also cover every handler the server exposes.
func TestOpenAPISpecIsValidAndRelative(t *testing.T) {
	var doc struct {
		OpenAPI string `json:"openapi"`
		Servers []struct {
			URL string `json:"url"`
		} `json:"servers"`
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal([]byte(openapiSpec), &doc); err != nil {
		t.Fatalf("openapiSpec is not valid JSON: %v", err)
	}
	if doc.OpenAPI != "3.0.0" {
		t.Errorf("openapi version = %q, want 3.0.0", doc.OpenAPI)
	}
	if len(doc.Servers) != 1 || doc.Servers[0].URL != "/" {
		t.Errorf("servers = %+v, want a single relative server {url:\"/\"} so the scaffolder targets the fetch host", doc.Servers)
	}
	// Every endpoint the handlers serve must be described, or a scaffolded
	// scenario would silently miss it. Keys are "METHOD /path".
	want := map[string]string{
		"get /browse":    "browse",
		"get /search":    "search",
		"get /category":  "category",
		"get /product":   "product",
		"post /cart":     "addToCart",
		"post /checkout": "checkout",
	}
	for route, opID := range want {
		method, path, _ := strings.Cut(route, " ")
		op, ok := doc.Paths[path][method]
		if !ok {
			t.Errorf("openapiSpec missing %s %s", strings.ToUpper(method), path)
			continue
		}
		if op.OperationID != opID {
			t.Errorf("%s %s operationId = %q, want %q", strings.ToUpper(method), path, op.OperationID, opID)
		}
	}
}
