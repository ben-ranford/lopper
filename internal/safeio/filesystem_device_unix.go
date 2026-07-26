//go:build unix

package safeio

import (
	"io/fs"
	"syscall"
)

// Unix builds enforce same-device traversal using st_dev from syscall.Stat_t.
// Build-tag contract: callers may rely on stable device identity being
// supported when this file is selected.
func sameDeviceIdentitySupported() bool {
	return true
}

func sameDeviceRootPair(_ Root, _ Root, left, right fs.FileInfo) bool {
	leftStat, leftOK := left.Sys().(*syscall.Stat_t)
	rightStat, rightOK := right.Sys().(*syscall.Stat_t)
	if !leftOK || !rightOK {
		return false
	}
	return leftStat.Dev == rightStat.Dev
}
