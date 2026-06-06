package domain

import "fmt"

// Segment is one behavioral profile ("persona") in an open-model run: a share of
// the arrivals that traverse the scenario graph with their own entry point and
// pacing. A run with several segments mixes distinct personas — say fast power
// users and slow first-time browsers — instead of one homogeneous user, which is
// what makes the simulated traffic organic rather than uniform.
//
// Every override is optional: a segment that sets only Name and Weight behaves
// like the run default but is still tallied under its own identity, so its share
// of the traffic is attributable.
type Segment struct {
	// Name labels the persona and tags its sessions in observations. Required and
	// unique within a run.
	Name string `json:"name"`
	// Weight is the segment's relative share of arrivals. Weights need not sum to
	// one; each segment's probability is its weight over the sum of all weights.
	Weight float64 `json:"weight"`
	// Start overrides the run's start node for this segment. Empty = run default,
	// so different personas can enter the journey at different points.
	Start ID `json:"start,omitempty"`
	// MaxSteps overrides the run's step bound for this segment. 0 = run default,
	// so a persona can take a longer or shorter journey.
	MaxSteps int `json:"maxSteps,omitempty"`
	// ThinkTime overrides the run's think time for this segment. nil = run
	// default, so a persona can act faster or slower between steps.
	ThinkTime *ThinkTime `json:"thinkTime,omitempty"`
}

// ValidateSegments checks a segment mix: names present and unique, weights
// positive, and any think-time override well-formed. An empty slice is valid —
// the run is then a single homogeneous persona.
func ValidateSegments(segs []Segment) error {
	seen := make(map[string]bool, len(segs))
	for i, s := range segs {
		if s.Name == "" {
			return fmt.Errorf("segment %d: name is required", i)
		}
		if seen[s.Name] {
			return fmt.Errorf("segment %q: duplicate name", s.Name)
		}
		seen[s.Name] = true
		if s.Weight <= 0 {
			return fmt.Errorf("segment %q: weight must be > 0 (got %v)", s.Name, s.Weight)
		}
		if s.MaxSteps < 0 {
			return fmt.Errorf("segment %q: maxSteps must be >= 0 (got %d)", s.Name, s.MaxSteps)
		}
		if s.ThinkTime != nil {
			if err := s.ThinkTime.Validate(); err != nil {
				return fmt.Errorf("segment %q: %w", s.Name, err)
			}
		}
	}
	return nil
}
