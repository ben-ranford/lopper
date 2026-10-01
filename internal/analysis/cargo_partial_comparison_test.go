package analysis

import "testing"

func TestCargoPartialComparisonOperators(t *testing.T) {
	cases := []struct {
		locked, required string
		// Expected results for >, >=, <, <= in that order.
		matches [4]bool
	}{
		{"0.9.9", "1", [4]bool{false, false, true, true}},
		{"1.9.9", "1", [4]bool{false, true, false, true}},
		{"2.0.0", "1", [4]bool{true, true, false, false}},
		{"1.1.9", "1.2", [4]bool{false, false, true, true}},
		{"1.2.9", "1.2", [4]bool{false, true, false, true}},
		{"1.3.0", "1.2", [4]bool{true, true, false, false}},
		{"0.9.9", "1.2.3", [4]bool{false, false, true, true}},
		{"2.0.0", "1.2.3", [4]bool{true, true, false, false}},
		{"1.1.9", "1.2.3", [4]bool{false, false, true, true}},
		{"1.3.0", "1.2.3", [4]bool{true, true, false, false}},
		{"1.2.2", "1.2.3", [4]bool{false, false, true, true}},
		{"1.2.3", "1.2.3", [4]bool{false, true, false, true}},
		{"1.2.4", "1.2.3", [4]bool{true, true, false, false}},
		{"1.2.3+build.2", "1.2.3+build.1", [4]bool{false, true, false, true}},
		{"1.2.3-alpha", "1.2.3-beta", [4]bool{false, false, true, true}},
		{"1.2.3-beta", "1.2.3-alpha", [4]bool{true, true, false, false}},
		{"1.2.3-beta", "1.2.3-beta", [4]bool{false, true, false, true}},
		{"1.2.3-beta", "1.2.3", [4]bool{false, false, true, true}},
		{"1.2.3", "1.2.3-beta", [4]bool{true, true, false, false}},
		// A partial bound is not an exact match for a prerelease. Inclusive
		// operators must retain their separate exact-match check.
		{"1.9.9-alpha", "1", [4]bool{}},
		{"1.2.9-alpha", "1.2", [4]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.locked+"/"+tc.required, func(t *testing.T) {
			locked, ok := parseCargoPartialVersion(tc.locked)
			if !ok {
				t.Fatalf("invalid locked fixture %q", tc.locked)
			}
			for index, operator := range []string{">", ">=", "<", "<="} {
				clause, ok := parseCargoRequirementClause(operator + tc.required)
				if !ok {
					t.Fatalf("invalid requirement fixture %q", operator+tc.required)
				}
				if got := cargoRequirementClauseMatches(clause, locked); got != tc.matches[index] {
					t.Errorf("%s %s %s = %t, want %t", tc.locked, operator, tc.required, got, tc.matches[index])
				}
			}
		})
	}
}
