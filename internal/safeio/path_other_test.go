//go:build !windows

package safeio

import (
	"path/filepath"
	"testing"
)

func TestResolveRelativeTargetAcceptsUnixNamesUnsafeOnWindows(t *testing.T) {
	for _, rawPath := range []string{`cache `, `cache.\child`, `CON`, `sub\NUL.txt`} {
		got, err := resolveRelativeTarget(rawPath, rejectRootTarget)
		if err != nil {
			t.Fatalf("resolveRelativeTarget(%q): %v", rawPath, err)
		}
		if want := filepath.Clean(rawPath); got != want {
			t.Fatalf("resolveRelativeTarget(%q) = %q, want %q", rawPath, got, want)
		}
	}
}
