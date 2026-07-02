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
	"unicode"

	"sigs.k8s.io/yaml"

	"github.com/chordpli/tmula/server/internal/scenariofile"
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

	// Derive the auth block (and how to inject the auth header) from the document's
	// security schemes. Best-effort: a doc with no scheme yields a nil derivation,
	// so an unsecured spec imports byte-for-byte as before. The login op (if any) is
	// found from the same operation list, so this runs after collectOperations.
	derived := deriveAuth(doc, ops)
	topLevel := doc.topLevelSecurity()

	flow := make([]scenariofile.Step, 0, len(ops))
	ids := newIDSet()
	for _, o := range ops {
		// A path with a space or control char would yield a malformed request line
		// ("METHOD /pa th"); drop it so the scenario stays runnable.
		if !safeRequestPath(o.path) {
			continue
		}
		step := scenariofile.Step{
			ID:      ids.unique(stepID(o.op.OperationID, o.method, o.path)),
			Request: strings.ToUpper(o.method) + " " + o.path,
			Body:    bodyExample(o.op),
		}
		// Inject the auth material on operations the security requirement covers (and
		// never on the login endpoint itself — minting a token is unauthenticated).
		// A query apiKey rides the request path; everything else is a header.
		if derived != nil && derived.secures(o.op, topLevel) && !isLoginRequest(derived, step.Request) {
			if derived.queryParam != "" {
				sep := "?"
				if strings.Contains(step.Request, "?") {
					sep = "&"
				}
				step.Request += sep + derived.queryParam + "={{.token|urlquery}}"
			} else {
				if step.Headers == nil {
					step.Headers = map[string]string{}
				}
				step.Headers[derived.header] = derived.headerValue
			}
		}
		flow = append(flow, step)
	}
	if len(flow) == 0 {
		return scenariofile.Scenario{}, fmt.Errorf("importer: openapi has no usable operations")
	}

	sc := scenariofile.Scenario{Target: openAPITarget(doc), Flow: flow}
	if derived != nil {
		sc.Auth = derived.auth
	}
	// Offer a signup suggestion when a register/signup operation exists, independent
	// of the primary auth above (a login may be the primary auth while a signup is
	// suggested separately). Best-effort: no register op yields no suggestion, so a
	// spec without one imports unchanged.
	sc.SuggestedSignup = deriveSignup(ops)
	return sc, nil
}

// isLoginRequest reports whether a step's "METHOD /path" matches the derived
// login flow's own step, so the importer does not inject a bearer header onto the
// endpoint that mints the token (it has no token yet).
func isLoginRequest(d *derivedAuth, request string) bool {
	if d.auth == nil || d.auth.Login == nil {
		return false
	}
	for _, st := range d.auth.Login.Flow {
		if st.Request == request {
			return true
		}
	}
	return false
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
		// Primary: a plausible user journey (land -> browse/search -> view ->
		// cart -> checkout) inferred from operationId/path keywords, so an imported
		// scenario reads like a real flow instead of alphabetically. Ties fall back
		// to the structural order (shallower paths first, then path, then method, so
		// safe reads precede writes).
		if si, sj := journeyStage(ops[i]), journeyStage(ops[j]); si != sj {
			return si < sj
		}
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

// journeyStage scores an operation by where it tends to fall in a user journey,
// using well-known keywords in its operationId and path. OpenAPI carries no flow
// information, so this turns an otherwise alphabetical dump into a sensible
// default order. The keywords span common domains (shopping, ticketing/booking,
// travel) but the model is generic — sign in, then read/list, then view one,
// then create/reserve, then pay — and any unrecognized operation falls to a
// neutral middle stage and keeps the structural order, so non-matching APIs are
// ordered reasonably too (and the user can always reorder afterwards). Lower is
// earlier.
func journeyStage(o apiOp) int {
	t := strings.ToLower(o.op.OperationID + " " + o.path)
	has := func(subs ...string) bool { return matchesAny(t, subs...) }
	switch {
	case matchesAny(t, loginKeywords...):
		return 0 // authenticate first
	// Checkout keywords stay commerce-specific so generic verbs (completeProfile,
	// confirmEmail) are not dragged to the end of an unrelated API.
	case has("checkout", "payment", "/pay", "purchase", "placeorder", "place-order", "/charge", "fulfil", "/order"):
		return 8 // pay / complete the journey last
	case has("cart", "basket", "/bag", "wishlist", "add-to", "addto", "addtocart", "reserve", "reservation", "booking"):
		return 6 // add to cart / reserve, just before paying
	case has("browse", "home", "landing", "/index", "/root", "dashboard", "welcome"):
		return 1 // land on an entry page first
	case has("search", "list", "catalog", "catalogue", "categor", "explore", "feed", "menu",
		"products", "items", "gallery", "event", "ticket", "flight", "hotel", "listing", "movie", "showtime", "trip", "room"):
		return 2 // browse / search a collection
	case has("detail", "view", "show", "/product", "/item", "{id}", "{slug}", "getone", "byid", "by-id", "seat", "availab", "slot"):
		return 4 // view a specific resource
	default:
		return 3 // unknown: a neutral middle
	}
}

// loginKeywords are the substrings (case-insensitive) that mark an operationId or
// path as an authentication / token-minting step. They drive journeyStage's "stage
// 0" ordering for OpenAPI and the HAR importer's login-request discovery, so the
// two paths agree on what a login endpoint looks like.
var loginKeywords = []string{
	"login", "signin", "sign-in", "logon", "oauth", "/token", "session", "register", "signup", "sign-up",
}

// matchesAny reports whether s contains any of subs.
func matchesAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
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
	// keptFor[i] is the flow index produced by entry i, or -1 if the entry was
	// dropped (cross-origin, malformed path). It lets the auth extractor map a HAR
	// entry (the login call, the auth-bearing requests) back onto the emitted steps.
	keptFor := make([]int, len(h.Log.Entries))
	ids := newIDSet()
	for i, e := range h.Log.Entries {
		keptFor[i] = -1
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
		// url.Parse decodes a percent-encoded control char (e.g. %0a) into a real
		// newline in u.Path; together with spaces these break the "METHOD /path"
		// request line, so drop the entry rather than emit a malformed step.
		if !safeRequestPath(path) {
			continue
		}
		method := strings.ToUpper(e.Request.Method)
		var body string
		if e.Request.PostData != nil {
			body = e.Request.PostData.Text
		}
		keptFor[i] = len(flow)
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

	sc := scenariofile.Scenario{Target: target, Flow: flow}
	// Auto-extract the captured auth: scan the entries for a live Authorization
	// header / auth cookie and a login request, emit a login or pool auth block,
	// and rewrite the replayed steps to carry Authorization: Bearer {{.token}}
	// instead of the stale captured literal. Best-effort and resilient: a HAR with
	// no auth yields no block, so an unauthenticated session imports unchanged.
	extractHARAuth(&sc, h.Log.Entries, keptFor)
	return sc, nil
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
	// Components.SecuritySchemes carries the named auth schemes (oauth2 flows, http
	// bearer, apiKey) the importer derives an auth block from. Left raw per-scheme so
	// a single malformed scheme is skipped rather than failing the whole parse.
	Components struct {
		SecuritySchemes map[string]json.RawMessage `json:"securitySchemes"`
	} `json:"components"`
	// Security is the top-level default security requirement applied to every
	// operation that does not override it. Raw so a malformed value is non-fatal.
	Security json.RawMessage `json:"security"`
}

type operation struct {
	OperationID string   `json:"operationId"`
	Tags        []string `json:"tags"`
	RequestBody *struct {
		Content map[string]struct {
			Example  json.RawMessage `json:"example"`
			Examples map[string]struct {
				Value json.RawMessage `json:"value"`
			} `json:"examples"`
		} `json:"content"`
	} `json:"requestBody"`
	// Security is the per-operation security requirement. A nil pointer means the
	// operation inherits the top-level default; a present (even empty) value
	// overrides it — an explicit `security: []` opts the operation out of auth.
	Security *[]securityRequirement `json:"security"`
}

// harDoc is the subset of a HAR file the importer reads. Beyond the request line
// and body (used to build a step), it now also reads request Headers and Cookies —
// to discover the live Authorization/auth-cookie credential — and the response
// Content/Headers, so a login entry can be recognized by a token in its response
// (via load.DetectCredential, reusing E1). Every added field is optional: a HAR
// that omits them parses exactly as before.
type harDoc struct {
	Log struct {
		Entries []harEntry `json:"entries"`
	} `json:"log"`
}

type harEntry struct {
	Request struct {
		Method   string  `json:"method"`
		URL      string  `json:"url"`
		Headers  []harNV `json:"headers"`
		Cookies  []harNV `json:"cookies"`
		PostData *struct {
			Text string `json:"text"`
		} `json:"postData"`
	} `json:"request"`
	Response struct {
		Headers []harNV `json:"headers"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"response"`
}

// harNV is a HAR name/value pair, the shape HAR uses for request/response headers
// and cookies.
type harNV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
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

// safeRequestPath reports whether a path is usable in a "METHOD /path" request
// line. The scenario request shorthand is split with strings.Fields, so any
// whitespace (a literal space, or a control char like the newline a
// percent-encoded %0a decodes into) would break the line into the wrong number
// of fields and fail confusingly later at parse time. Such paths are dropped at
// import so the generated scenario only ever carries well-formed request lines.
func safeRequestPath(path string) bool {
	for _, r := range path {
		if r == ' ' || unicode.IsControl(r) {
			return false
		}
	}
	return true
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
