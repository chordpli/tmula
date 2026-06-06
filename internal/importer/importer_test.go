package importer

import (
	"strings"
	"testing"

	"github.com/chordpli/tmula/internal/scenariofile"
)

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
