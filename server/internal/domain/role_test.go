package domain

import "testing"

func TestParseRole(t *testing.T) {
	cases := []struct {
		in   string
		want Role
		ok   bool
	}{
		{"local", RoleLocal, true},
		{"MASTER", RoleMaster, true},
		{" worker ", RoleWorker, true},
		{"bogus", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, err := ParseRole(tc.in)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Errorf("ParseRole(%q) = %q, %v; want %q, nil", tc.in, got, err, tc.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("ParseRole(%q): expected error, got %q", tc.in, got)
		}
	}
}
