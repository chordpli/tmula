package load

import (
	"encoding/json"
	"math/rand"
	"sort"
)

// Mutation turns a single JSON value into an abnormal one — the kind of input a
// developer often forgets to guard against. The set is a plain slice so callers
// can extend it without a new interface.
type Mutation struct {
	Name  string
	Apply func(value any, rng *rand.Rand) any
}

// DefaultMutations are the built-in payload mutations (boundary / type / null).
var DefaultMutations = []Mutation{
	{"null", func(any, *rand.Rand) any { return nil }},
	{"empty-string", func(any, *rand.Rand) any { return "" }},
	{"huge-number", func(any, *rand.Rand) any { return 1e308 }},
	{"negative", func(any, *rand.Rand) any { return -999999 }},
	{"type-swap", func(v any, _ *rand.Rand) any {
		switch v.(type) {
		case string:
			return 0
		case float64, int:
			return "not-a-number"
		case bool:
			return "maybe"
		default:
			return 0
		}
	}},
}

// MutationResult describes what Mutate changed.
type MutationResult struct {
	Mutated  bool
	Field    string
	Mutation string
}

// Mutate parses a JSON object payload, picks one field and applies one mutation
// (both chosen via rng), and returns the new payload. A payload that is not a
// non-empty JSON object is returned unchanged with Mutated=false. The chosen
// mutation set must be non-empty.
func Mutate(payload []byte, muts []Mutation, rng *rand.Rand) ([]byte, MutationResult) {
	if len(muts) == 0 {
		return payload, MutationResult{}
	}
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil || len(obj) == 0 {
		return payload, MutationResult{}
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys) // stable set; rng decides the choice

	k := keys[rng.Intn(len(keys))]
	m := muts[rng.Intn(len(muts))]
	obj[k] = m.Apply(obj[k], rng)

	out, err := json.Marshal(obj)
	if err != nil {
		return payload, MutationResult{}
	}
	return out, MutationResult{Mutated: true, Field: k, Mutation: m.Name}
}
