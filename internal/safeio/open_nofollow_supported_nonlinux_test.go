//go:build !linux

package safeio

import "testing"

func TestOpenFileNoFollowSupportedFailsClosedOnNonLinux(t *testing.T) {
	if OpenFileNoFollowSupported() {
		t.Fatal("expected non-linux no-follow support probe to fail closed")
	}
}
