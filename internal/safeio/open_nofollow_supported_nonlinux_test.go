//go:build !(linux || darwin || windows)

package safeio

import "testing"

func TestOpenFileNoFollowSupportedFailsClosedOnUnsupportedPlatforms(t *testing.T) {
	if OpenFileNoFollowSupported() {
		t.Fatal("expected unsupported platform no-follow support to fail closed")
	}
}
