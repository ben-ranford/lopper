//go:build !unix && !windows

package safeio

import (
	"io/fs"
	"testing"
)

func TestSameDeviceIdentityContractOnUnsupportedPlatforms(t *testing.T) {
	if sameDeviceIdentitySupported() {
		t.Fatal("unsupported platform unexpectedly reports stable device identity support")
	}

	info := &sysOverrideFileInfo{sys: nil}
	if sameDeviceRootPair(nil, nil, info, info) {
		t.Fatal("same-device proof must fail closed when unsupported platforms cannot prove identity")
	}
}

func TestEnforceSameDeviceBoundaryAllowsTraversalWhenUnsupported(t *testing.T) {
	previous := sameDeviceFileInfoFn
	sameDeviceFileInfoFn = func(Root, Root, fs.FileInfo, fs.FileInfo) bool { return false }
	t.Cleanup(func() {
		sameDeviceFileInfoFn = previous
	})

	if !enforceSameDeviceBoundary(nil, nil, nil, nil) {
		t.Fatal("generic traversal should stay enabled when stable device identity is unsupported")
	}
}
