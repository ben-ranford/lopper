package mcp

import "testing"

func TestTraversalComponentsPreserveUnicodePathBoundaries(t *testing.T) {
	for _, tc := range []struct {
		path      string
		traversal bool
	}{
		{"", false}, {" \t", false}, {"日本語/cache", false}, {"日本語/../cache", true},
		{"日本語/..", true}, {"日本語/．．/cache", false}, {"日本語/..cache", false}, {"..日本語cache", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := pathContainsTraversalComponent(tc.path); got != tc.traversal {
				t.Fatalf("path %q traversal = %t, want %t", tc.path, got, tc.traversal)
			}
		})
	}
}
