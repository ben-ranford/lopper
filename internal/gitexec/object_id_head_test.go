package gitexec

import (
	"strings"
	"testing"
)

func TestValidObjectID(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		value string
		valid bool
	}{
		{"sha1 lowercase", strings.Repeat("0123456789abcdef", 2) + "01234567", true},
		{"sha1 uppercase", strings.Repeat("A", 40), true},
		{"sha256 mixed case", strings.Repeat("aBcDeF09", 8), true},
		{"empty", "", false},
		{"abbreviated", strings.Repeat("a", 39), false},
		{"oversized sha1", strings.Repeat("a", 41), false},
		{"short sha256", strings.Repeat("a", 63), false},
		{"oversized sha256", strings.Repeat("a", 65), false},
		{"ref name", "refs/heads/proof-base", false},
		{"nonhex", strings.Repeat("a", 39) + "g", false},
		{"unicode", strings.Repeat("a", 38) + "é", false},
		{"line break", strings.Repeat("a", 39) + "\n", false},
		{"option", "-" + strings.Repeat("a", 39), false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ValidObjectID(testCase.value); got != testCase.valid {
				t.Fatalf("ValidObjectID(%q) = %v, want %v", testCase.value, got, testCase.valid)
			}
		})
	}
}
