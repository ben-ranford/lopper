package safeio

import (
	"io/fs"
	"testing"
)

type sysOverrideFileInfo struct {
	fs.FileInfo
	sys any
}

func (i *sysOverrideFileInfo) Sys() any {
	return i.sys
}

func TestSameDeviceRootPairFailsClosedWithoutProvableIdentity(t *testing.T) {
	info := &sysOverrideFileInfo{
		FileInfo: statTestPath(t, t.TempDir()),
		sys:      nil,
	}
	if sameDeviceRootPair(nil, nil, info, info) {
		t.Fatal("expected same-device identity to fail closed when it cannot be proven")
	}
}
