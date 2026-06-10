package domain

import "testing"

func TestValidateSegments(t *testing.T) {
	tt := []ThinkTime{{MinMs: 0, MaxMs: 100}}
	cases := []struct {
		name    string
		segs    []Segment
		wantErr bool
	}{
		{"empty is valid", nil, false},
		{"single weighted", []Segment{{Name: "a", Weight: 1}}, false},
		{"mix with overrides", []Segment{
			{Name: "browser", Weight: 0.7, Start: "browse", ThinkTime: &tt[0]},
			{Name: "buyer", Weight: 0.3, Start: "cart", MaxSteps: 12},
		}, false},
		{"missing name", []Segment{{Weight: 1}}, true},
		{"duplicate name", []Segment{{Name: "a", Weight: 1}, {Name: "a", Weight: 1}}, true},
		{"zero weight", []Segment{{Name: "a", Weight: 0}}, true},
		{"negative weight", []Segment{{Name: "a", Weight: -1}}, true},
		{"negative maxSteps", []Segment{{Name: "a", Weight: 1, MaxSteps: -1}}, true},
		{"bad think time", []Segment{{Name: "a", Weight: 1, ThinkTime: &ThinkTime{MinMs: 50, MaxMs: 10}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateSegments(c.segs)
			if (err != nil) != c.wantErr {
				t.Errorf("ValidateSegments(%v) err = %v, wantErr = %v", c.segs, err, c.wantErr)
			}
		})
	}
}
