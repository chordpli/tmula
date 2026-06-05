package load

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMutateChangesObject(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	orig := []byte(`{"qty":5,"name":"widget"}`)
	mutated, res := Mutate(orig, DefaultMutations, rng)
	if !res.Mutated {
		t.Fatal("expected a mutation")
	}
	if string(mutated) == string(orig) {
		t.Fatalf("payload unchanged: %s", mutated)
	}
	// Output must still be valid JSON.
	var obj map[string]any
	if err := json.Unmarshal(mutated, &obj); err != nil {
		t.Fatalf("mutated payload is not valid json: %v", err)
	}
	if res.Field != "qty" && res.Field != "name" {
		t.Errorf("unexpected mutated field %q", res.Field)
	}
}

func TestMutateNonObjectPassthrough(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, in := range []string{`[1,2,3]`, `"plain"`, `42`, `not json`, `{}`} {
		out, res := Mutate([]byte(in), DefaultMutations, rng)
		if res.Mutated {
			t.Errorf("input %q should not be mutated", in)
		}
		if string(out) != in {
			t.Errorf("passthrough changed %q to %q", in, out)
		}
	}
}

func TestMutateEmptyMutationSet(t *testing.T) {
	out, res := Mutate([]byte(`{"a":1}`), nil, rand.New(rand.NewSource(1)))
	if res.Mutated || string(out) != `{"a":1}` {
		t.Fatal("empty mutation set must be a no-op")
	}
}

func TestMutationCoversFields(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		_, res := Mutate([]byte(`{"a":1,"b":2,"c":3}`), DefaultMutations, rng)
		if res.Mutated {
			seen[res.Field] = true
		}
	}
	for _, f := range []string{"a", "b", "c"} {
		if !seen[f] {
			t.Errorf("mutation never touched field %q over 200 runs", f)
		}
	}
}

// TestMutationSurfacesServerError is the #5 AC: a mutated payload that the
// happy path would never produce drives the server to a 4xx.
func TestMutationSurfacesServerError(t *testing.T) {
	// SUT requires qty to be a JSON number; anything else is a 400.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, ok := body["qty"].(float64); !ok {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	adapter := NewRESTAdapter(2 * time.Second)

	// Baseline: a valid payload is accepted.
	ok, err := adapter.Send(context.Background(), RenderedRequest{
		Method: "POST", URL: srv.URL, Body: []byte(`{"qty":3}`),
	})
	if err != nil || ok.StatusCode != http.StatusOK {
		t.Fatalf("baseline should be 200, got %d (%v)", ok.StatusCode, err)
	}

	// Force the qty field to a non-number; the server must reject it.
	typeSwap := []Mutation{{"force-string", func(any, *rand.Rand) any { return "oops" }}}
	mutated, res := Mutate([]byte(`{"qty":3}`), typeSwap, rand.New(rand.NewSource(1)))
	if !res.Mutated {
		t.Fatal("expected mutation to apply")
	}
	bad, err := adapter.Send(context.Background(), RenderedRequest{
		Method: "POST", URL: srv.URL, Body: mutated,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("mutated payload should surface a 400, got %d", bad.StatusCode)
	}
}
