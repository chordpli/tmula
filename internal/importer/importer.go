// Package importer scaffolds a tmula scenario from an existing API description —
// an OpenAPI document or a recorded HAR file — so a user does not have to author
// the flow by hand. The output is a scenariofile.Scenario the operator then
// edits and runs; it is a starting point, not a finished load test.
//
// It deliberately parses only the subset of each format it needs into local
// structs (decoded with the already-vendored sigs.k8s.io/yaml / encoding/json),
// so it adds no new dependency.
package importer

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/chordpli/tmula/internal/scenariofile"
)

// httpMethods are the path-item keys treated as operations (OpenAPI mixes
// methods with non-operation keys like "parameters" under a path).
var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
}

// FromOpenAPI builds a scenario from an OpenAPI 3 (servers) or Swagger 2
// (host/basePath) document in YAML or JSON. Each path+method becomes a flow
// step. The target is the first server URL when absolute; otherwise the caller
// must supply one (Scenario.Target is left blank).
func FromOpenAPI(data []byte) (scenariofile.Scenario, error) {
	var doc openAPIDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return scenariofile.Scenario{}, fmt.Errorf("importer: parse openapi: %w", err)
	}
	if len(doc.Paths) == 0 {
		return scenariofile.Scenario{}, fmt.Errorf("importer: openapi has no paths")
	}

	ops := collectOperations(doc.Paths)
	if len(ops) == 0 {
		return scenariofile.Scenario{}, fmt.Errorf("importer: openapi has no usable operations")
	}
	flow := make([]scenariofile.Step, 0, len(ops))
	ids := newIDSet()
	for _, o := range ops {
		flow = append(flow, scenariofile.Step{
			ID:      ids.unique(stepID(o.op.OperationID, o.method, o.path)),
			Request: strings.ToUpper(o.method) + " " + o.path,
			Body:    bodyExample(o.op),
		})
	}

	return scenariofile.Scenario{Target: openAPITarget(doc), Flow: flow}, nil
}

// apiOp is one OpenAPI operation flattened out of the path→method map.
type apiOp struct {
	path   string
	method string
	op     operation
}

// collectOperations flattens the path→method map into operations ordered for a
// plausible first-pass journey: shallower paths before nested ones, then
// alphabetical by path, then reads-before-writes within a path (GET, POST, PUT,
// PATCH, DELETE). OpenAPI carries no sequencing, so this is only a scaffold
// order the operator reorders to match the real flow.
func collectOperations(paths map[string]map[string]json.RawMessage) []apiOp {
	var ops []apiOp
	for path, methods := range paths {
		for method, raw := range methods {
			if !httpMethods[strings.ToLower(method)] {
				continue
			}
			var op operation
			_ = json.Unmarshal(raw, &op) // best-effort; only operationId/body used
			ops = append(ops, apiOp{path: path, method: strings.ToLower(method), op: op})
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		if di, dj := pathDepth(ops[i].path), pathDepth(ops[j].path); di != dj {
			return di < dj
		}
		if ops[i].path != ops[j].path {
			return ops[i].path < ops[j].path
		}
		return methodOrder(ops[i].method) < methodOrder(ops[j].method)
	})
	return ops
}

// methodOrder ranks HTTP methods reads-before-writes, destructive last.
func methodOrder(method string) int {
	switch method {
	case "get":
		return 0
	case "post":
		return 1
	case "put":
		return 2
	case "patch":
		return 3
	case "delete":
		return 4
	default:
		return 5
	}
}

// pathDepth counts a path's segments, so parent resources sort before children.
func pathDepth(p string) int {
	p = strings.Trim(p, "/")
	if p == "" {
		return 0
	}
	return strings.Count(p, "/") + 1
}

// FromHAR builds a scenario from a recorded HAR file: each captured request
// becomes a flow step, in order, so the scenario replays the session. Only
// requests sharing the first request's origin are kept (third-party/asset calls
// are dropped). The target is that origin.
func FromHAR(data []byte) (scenariofile.Scenario, error) {
	var h harDoc
	if err := json.Unmarshal(data, &h); err != nil {
		return scenariofile.Scenario{}, fmt.Errorf("importer: parse har: %w", err)
	}
	if len(h.Log.Entries) == 0 {
		return scenariofile.Scenario{}, fmt.Errorf("importer: har has no entries")
	}

	var target string
	flow := make([]scenariofile.Step, 0, len(h.Log.Entries))
	ids := newIDSet()
	for _, e := range h.Log.Entries {
		u, err := url.Parse(e.Request.URL)
		if err != nil || u.Host == "" {
			continue
		}
		origin := u.Scheme + "://" + u.Host
		if target == "" {
			target = origin
		}
		if origin != target {
			continue // drop cross-origin (analytics, CDNs, ...)
		}
		path := u.Path
		if path == "" {
			path = "/"
		}
		if u.RawQuery != "" {
			path += "?" + u.RawQuery
		}
		method := strings.ToUpper(e.Request.Method)
		var body string
		if e.Request.PostData != nil {
			body = e.Request.PostData.Text
		}
		flow = append(flow, scenariofile.Step{
			// Derive the id from path+query so two calls to the same path with
			// different queries (/search?q=1 vs ?q=2) get distinct ids.
			ID:      ids.unique(stepID("", method, path)),
			Request: method + " " + path,
			Body:    body,
		})
	}
	if len(flow) == 0 {
		return scenariofile.Scenario{}, fmt.Errorf("importer: har has no usable requests")
	}
	return scenariofile.Scenario{Target: target, Flow: flow}, nil
}

// Marshal renders a scenario as a YAML document for writing to a file. It round-
// trips through the json tags (sigs.k8s.io/yaml), so field names match the CLI.
func Marshal(s scenariofile.Scenario) ([]byte, error) {
	b, err := yaml.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("importer: marshal scenario: %w", err)
	}
	return b, nil
}

// --- minimal format structs ---

type openAPIDoc struct {
	Servers []struct {
		URL string `json:"url"`
	} `json:"servers"`
	Host     string                                `json:"host"`     // swagger 2
	BasePath string                                `json:"basePath"` // swagger 2
	Schemes  []string                              `json:"schemes"`  // swagger 2
	Paths    map[string]map[string]json.RawMessage `json:"paths"`
}

type operation struct {
	OperationID string `json:"operationId"`
	RequestBody *struct {
		Content map[string]struct {
			Example  json.RawMessage `json:"example"`
			Examples map[string]struct {
				Value json.RawMessage `json:"value"`
			} `json:"examples"`
		} `json:"content"`
	} `json:"requestBody"`
}

type harDoc struct {
	Log struct {
		Entries []struct {
			Request struct {
				Method   string `json:"method"`
				URL      string `json:"url"`
				PostData *struct {
					Text string `json:"text"`
				} `json:"postData"`
			} `json:"request"`
		} `json:"entries"`
	} `json:"log"`
}

// openAPITarget derives a base URL from a v3 server or a v2 host/basePath. A
// relative or absent server yields "" — the caller then requires --target.
func openAPITarget(doc openAPIDoc) string {
	if len(doc.Servers) > 0 {
		if u, err := url.Parse(doc.Servers[0].URL); err == nil && u.IsAbs() {
			return strings.TrimSuffix(doc.Servers[0].URL, "/")
		}
	}
	if doc.Host != "" {
		scheme := "https"
		if len(doc.Schemes) > 0 {
			scheme = doc.Schemes[0]
		}
		return strings.TrimSuffix(scheme+"://"+doc.Host+doc.BasePath, "/")
	}
	return ""
}

// bodyExample returns a compact JSON body from an operation's requestBody
// example, preferring an application/json example, else the first one found.
func bodyExample(op operation) string {
	if op.RequestBody == nil {
		return ""
	}
	pick := func(raw json.RawMessage) string {
		if len(raw) == 0 {
			return ""
		}
		return string(compactJSON(raw))
	}
	if mt, ok := op.RequestBody.Content["application/json"]; ok {
		if s := pick(mt.Example); s != "" {
			return s
		}
		for _, ex := range mt.Examples {
			if s := pick(ex.Value); s != "" {
				return s
			}
		}
	}
	for _, mt := range op.RequestBody.Content {
		if s := pick(mt.Example); s != "" {
			return s
		}
	}
	return ""
}

func compactJSON(raw json.RawMessage) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

// stepID builds a stable, sanitized id from an operationId (preferred) or the
// method and path, e.g. ("", "GET", "/users/{id}") -> "get_users_id".
func stepID(operationID, method, path string) string {
	if operationID != "" {
		return sanitize(operationID)
	}
	return sanitize(strings.ToLower(method) + "_" + path)
}

func sanitize(s string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "step"
	}
	return out
}

// idSet hands out unique ids, suffixing collisions with _2, _3, ...
type idSet struct{ seen map[string]int }

func newIDSet() *idSet { return &idSet{seen: map[string]int{}} }

func (s *idSet) unique(id string) string {
	s.seen[id]++
	if n := s.seen[id]; n > 1 {
		return fmt.Sprintf("%s_%d", id, n)
	}
	return id
}
