package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chordpli/tmula/server/internal/scenariofile"
)

// TestExampleImportFilesParse keeps the shipped import examples
// (examples/imports) valid: they must parse into a non-trivial scenario so the
// "import this file" docs and UI demo never point at a broken sample.
func TestExampleImportFilesParse(t *testing.T) {
	importsDir := filepath.Join("..", "..", "..", "examples", "imports")

	oa, err := os.ReadFile(filepath.Join(importsDir, "shop.openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi example: %v", err)
	}
	if sc, err := FromOpenAPI(oa); err != nil {
		t.Fatalf("import openapi example: %v", err)
	} else if len(sc.Flow) < 6 {
		t.Errorf("openapi example flow = %d steps, want >= 6", len(sc.Flow))
	}

	har, err := os.ReadFile(filepath.Join(importsDir, "shop-session.har"))
	if err != nil {
		t.Fatalf("read har example: %v", err)
	}
	if sc, err := FromHAR(har); err != nil {
		t.Fatalf("import har example: %v", err)
	} else if len(sc.Flow) < 5 {
		t.Errorf("har example flow = %d steps, want >= 5", len(sc.Flow))
	}

	tix, err := os.ReadFile(filepath.Join(importsDir, "ticketing.openapi.yaml"))
	if err != nil {
		t.Fatalf("read ticketing example: %v", err)
	}
	if sc, err := FromOpenAPI(tix); err != nil {
		t.Fatalf("import ticketing example: %v", err)
	} else if len(sc.Flow) < 5 {
		t.Errorf("ticketing example flow = %d steps, want >= 5", len(sc.Flow))
	}
}

const openAPIv3 = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
paths:
  /health:
    get: {}
  /users:
    get:
      operationId: listUsers
    post:
      operationId: createUser
      requestBody:
        content:
          application/json:
            example: { "name": "alice" }
`

func TestFromOpenAPIv3(t *testing.T) {
	s, err := FromOpenAPI([]byte(openAPIv3))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Target != "http://api.example.com" {
		t.Errorf("target = %q", s.Target)
	}
	if len(s.Flow) != 3 {
		t.Fatalf("flow = %d steps, want 3", len(s.Flow))
	}
	// The POST /users step carries the example body.
	var post *scenariofile.Step
	for i := range s.Flow {
		if s.Flow[i].Request == "POST /users" {
			post = &s.Flow[i]
		}
	}
	if post == nil {
		t.Fatal("no POST /users step")
	}
	if post.ID != "createUser" {
		t.Errorf("post id = %q, want createUser (from operationId)", post.ID)
	}
	if !strings.Contains(post.Body, "alice") {
		t.Errorf("post body = %q, want the example with alice", post.Body)
	}

	// The imported scenario must expand into a runnable spec.
	if _, err := scenariofile.Expand(s); err != nil {
		t.Errorf("expand imported scenario: %v", err)
	}
}

func TestFromOpenAPISwagger2(t *testing.T) {
	const doc = `
swagger: "2.0"
host: api.example.com
basePath: /v1
schemes: [https]
paths:
  /ping:
    get: {}
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if s.Target != "https://api.example.com/v1" {
		t.Errorf("target = %q, want https://api.example.com/v1", s.Target)
	}
	if len(s.Flow) != 1 || s.Flow[0].Request != "GET /ping" {
		t.Errorf("flow = %+v", s.Flow)
	}
}

func TestFromHAR(t *testing.T) {
	const har = `{"log":{"entries":[
    {"request":{"method":"GET","url":"http://app.test/home"}},
    {"request":{"method":"POST","url":"http://app.test/login","postData":{"text":"{\"u\":\"a\"}"}}},
    {"request":{"method":"GET","url":"http://cdn.other/asset.js"}}
  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if s.Target != "http://app.test" {
		t.Errorf("target = %q, want http://app.test", s.Target)
	}
	if len(s.Flow) != 2 {
		t.Fatalf("flow = %d steps, want 2 (cross-origin dropped)", len(s.Flow))
	}
	if s.Flow[0].Request != "GET /home" || s.Flow[1].Request != "POST /login" {
		t.Errorf("flow order wrong: %+v", s.Flow)
	}
	if s.Flow[1].Body != `{"u":"a"}` {
		t.Errorf("login body = %q", s.Flow[1].Body)
	}
}

func TestOpenAPIGenericVerbsNotCheckout(t *testing.T) {
	// Generic verbs like completeProfile must NOT be ranked as checkout (and shoved
	// to the end) just because they contain "complete"/"confirm". They get the
	// neutral middle stage, so they sort before a real checkout.
	const doc = `
openapi: 3.0.0
servers:
  - url: http://h
paths:
  /catalog:
    get: { operationId: listCatalog }
  /profile/complete:
    post: { operationId: completeProfile }
  /checkout:
    post: { operationId: checkout }
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	want := []string{"GET /catalog", "POST /profile/complete", "POST /checkout"}
	for i, w := range want {
		if s.Flow[i].Request != w {
			t.Errorf("step %d = %q, want %q (a generic 'complete' verb must not rank as checkout)", i, s.Flow[i].Request, w)
		}
	}
}

func TestOpenAPIKeywordJourneyOrder(t *testing.T) {
	// Paths are declared scrambled; the importer should reorder them into a
	// plausible shopping journey from the operationId/path keywords, not
	// alphabetically (which would give addToCart, browse, category, ...).
	const doc = `
openapi: 3.0.0
servers:
  - url: http://shop
paths:
  /checkout:
    post: { operationId: checkout }
  /cart:
    post: { operationId: addToCart }
  /product:
    get: { operationId: product }
  /search:
    get: { operationId: search }
  /browse:
    get: { operationId: browse }
  /category:
    get: { operationId: category }
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	want := []string{
		"GET /browse",   // land
		"GET /category", // browse a collection
		"GET /search",
		"GET /product", // view a specific item
		"POST /cart",   // add to cart
		"POST /checkout",
	}
	if len(s.Flow) != len(want) {
		t.Fatalf("flow = %d steps, want %d", len(s.Flow), len(want))
	}
	for i, w := range want {
		if s.Flow[i].Request != w {
			t.Errorf("step %d = %q, want %q", i, s.Flow[i].Request, w)
		}
	}
}

func TestOpenAPITicketingJourneyOrder(t *testing.T) {
	// A non-shopping (ticketing) API must also order into a sensible journey:
	// sign in -> browse events -> view seats -> reserve -> pay.
	const doc = `
openapi: 3.0.0
servers:
  - url: http://tix
paths:
  /checkout:
    post: { operationId: checkout }
  /reservations:
    post: { operationId: reserve }
  /events/{id}/seats:
    get: { operationId: seats }
  /events:
    get: { operationId: listEvents }
  /login:
    post: { operationId: login }
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	want := []string{
		"POST /login",            // authenticate
		"GET /events",            // browse the collection
		"GET /events/{id}/seats", // same stage, deeper path sorts after
		"POST /reservations",     // reserve
		"POST /checkout",         // pay last
	}
	if len(s.Flow) != len(want) {
		t.Fatalf("flow = %d steps, want %d", len(s.Flow), len(want))
	}
	for i, w := range want {
		if s.Flow[i].Request != w {
			t.Errorf("step %d = %q, want %q", i, s.Flow[i].Request, w)
		}
	}
}

func TestOpenAPIJourneyOrder(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://h
paths:
  /users/{id}:
    get: {}
  /users:
    delete: {}
    get: {}
    post: {}
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	// Shallower path first; within /users reads before writes (GET, POST,
	// DELETE); then the nested /users/{id}.
	want := []string{"GET /users", "POST /users", "DELETE /users", "GET /users/{id}"}
	if len(s.Flow) != len(want) {
		t.Fatalf("flow = %d steps, want %d", len(s.Flow), len(want))
	}
	for i, w := range want {
		if s.Flow[i].Request != w {
			t.Errorf("step %d = %q, want %q", i, s.Flow[i].Request, w)
		}
	}
}

func TestHARStepIDIncludesQuery(t *testing.T) {
	const har = `{"log":{"entries":[
    {"request":{"method":"GET","url":"http://h/search?q=1"}},
    {"request":{"method":"GET","url":"http://h/search?q=2"}}
  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if len(s.Flow) != 2 {
		t.Fatalf("flow = %d, want 2", len(s.Flow))
	}
	if s.Flow[0].ID == s.Flow[1].ID {
		t.Errorf("ids collided: both %q (query should distinguish them)", s.Flow[0].ID)
	}
	if !strings.Contains(s.Flow[0].ID, "1") || !strings.Contains(s.Flow[1].ID, "2") {
		t.Errorf("ids %q / %q should reflect the query values", s.Flow[0].ID, s.Flow[1].ID)
	}
}

func TestMarshalRoundtrip(t *testing.T) {
	s, err := FromOpenAPI([]byte(openAPIv3))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	data, err := Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := scenariofile.Parse(data)
	if err != nil {
		t.Fatalf("re-parse marshaled scenario: %v", err)
	}
	if back.Target != s.Target || len(back.Flow) != len(s.Flow) {
		t.Errorf("roundtrip mismatch: target %q vs %q, flow %d vs %d",
			back.Target, s.Target, len(back.Flow), len(s.Flow))
	}
}

func TestImportRejectsEmpty(t *testing.T) {
	if _, err := FromOpenAPI([]byte(`openapi: 3.0.0`)); err == nil {
		t.Error("openapi with no paths should error")
	}
	if _, err := FromHAR([]byte(`{"log":{"entries":[]}}`)); err == nil {
		t.Error("har with no entries should error")
	}
}

// TestHARDropsNewlineInPath covers a HAR URL whose percent-encoded control char
// (%0a) decodes into a real newline in the path. Such a step would produce a
// malformed "METHOD /path" request line, so it must be dropped while the clean
// entry survives and the result still expands.
func TestHARDropsNewlineInPath(t *testing.T) {
	const har = `{"log":{"entries":[
	    {"request":{"method":"GET","url":"http://app.test/ok"}},
	    {"request":{"method":"GET","url":"http://app.test/bad%0apath"}}
	  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if len(s.Flow) != 1 {
		t.Fatalf("flow = %d steps, want 1 (newline-in-path dropped): %+v", len(s.Flow), s.Flow)
	}
	if s.Flow[0].Request != "GET /ok" {
		t.Errorf("kept step = %q, want GET /ok", s.Flow[0].Request)
	}
	if strings.ContainsAny(s.Flow[0].Request, "\r\n\t") {
		t.Errorf("request line carries a control char: %q", s.Flow[0].Request)
	}
	// The sanitized scenario must still expand into a runnable spec.
	if _, err := scenariofile.Expand(s); err != nil {
		t.Errorf("expand sanitized scenario: %v", err)
	}
}

// TestHARDropsSpaceInPath covers a HAR URL with a literal space in the path,
// which would split the request line into too many fields. The clean entry is
// kept.
func TestHARDropsSpaceInPath(t *testing.T) {
	const har = `{"log":{"entries":[
	    {"request":{"method":"GET","url":"http://app.test/clean"}},
	    {"request":{"method":"GET","url":"http://app.test/has space"}}
	  ]}}`
	s, err := FromHAR([]byte(har))
	if err != nil {
		t.Fatalf("FromHAR: %v", err)
	}
	if len(s.Flow) != 1 || s.Flow[0].Request != "GET /clean" {
		t.Fatalf("flow = %+v, want only GET /clean (space-in-path dropped)", s.Flow)
	}
}

// TestOpenAPIDropsSpaceInPath covers an OpenAPI path containing a space. The
// operation is dropped; a clean sibling path survives and the result expands.
func TestOpenAPIDropsSpaceInPath(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
paths:
  "/good":
    get: {}
  "/bad path":
    get: {}
`
	s, err := FromOpenAPI([]byte(doc))
	if err != nil {
		t.Fatalf("FromOpenAPI: %v", err)
	}
	if len(s.Flow) != 1 || s.Flow[0].Request != "GET /good" {
		t.Fatalf("flow = %+v, want only GET /good (space-in-path dropped)", s.Flow)
	}
	if _, err := scenariofile.Expand(s); err != nil {
		t.Errorf("expand sanitized scenario: %v", err)
	}
}

// TestOpenAPIAllPathsUnsafeErrors covers the case where every path is malformed:
// the importer must report no usable operations rather than return an empty flow.
func TestOpenAPIAllPathsUnsafeErrors(t *testing.T) {
	const doc = `
openapi: 3.0.0
servers:
  - url: http://api.example.com
paths:
  "/bad path":
    get: {}
`
	if _, err := FromOpenAPI([]byte(doc)); err == nil {
		t.Error("openapi whose only path is malformed should error (no usable operations)")
	}
}
