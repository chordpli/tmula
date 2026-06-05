// Package scenario parses and validates scenario graph definitions
// (YAML or JSON) into the domain model. It enforces transition-weight bounds
// and that dependency edges form a DAG (no required predecessor is part of a
// cycle), so the engine can traverse the graph without ever skipping a
// required step.
package scenario

import (
	"encoding/json"
	"fmt"

	"github.com/chordpli/tmula/internal/domain"
	"sigs.k8s.io/yaml"
)

// Format is a graph definition serialization format.
type Format string

const (
	FormatJSON Format = "json"
	FormatYAML Format = "yaml"
)

// Parse decodes a scenario graph definition in the given format and validates
// it. A returned error means the definition must not be run.
func Parse(data []byte, format Format) (domain.ScenarioGraph, error) {
	var g domain.ScenarioGraph
	switch format {
	case FormatJSON:
		if err := json.Unmarshal(data, &g); err != nil {
			return domain.ScenarioGraph{}, fmt.Errorf("scenario: parse json: %w", err)
		}
	case FormatYAML:
		// sigs.k8s.io/yaml converts YAML to JSON first, so it honors the json
		// struct tags on the domain model (single source of field names).
		if err := yaml.Unmarshal(data, &g); err != nil {
			return domain.ScenarioGraph{}, fmt.Errorf("scenario: parse yaml: %w", err)
		}
	default:
		return domain.ScenarioGraph{}, fmt.Errorf("scenario: unknown format %q", format)
	}
	if err := Validate(g); err != nil {
		return domain.ScenarioGraph{}, err
	}
	return g, nil
}

// MarshalJSON serializes a graph back to indented JSON (round-trip / storage).
func MarshalJSON(g domain.ScenarioGraph) ([]byte, error) {
	return json.MarshalIndent(g, "", "  ")
}
