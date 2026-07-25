//go:build unix && !linux && !darwin

package safeio

import "testing"

func TestOpenFileNoFollowUnsupportedUnixBuilds(t *testing.T) {
	if OpenFileNoFollowSupported() {
		t.Fatal("expected unsupported unix targets to fail closed")
	}
}
