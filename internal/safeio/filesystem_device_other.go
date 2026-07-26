//go:build !unix && !windows

package safeio

import "io/fs"

// Platforms selected by !unix && !windows do not currently expose a stable
// device or volume identity contract in safeio. Generic rooted traversal must
// preserve the preexisting nested read/write behavior there, while any feature
// that explicitly requires device proof must fail closed by checking
// sameDeviceIdentitySupported first.
func sameDeviceIdentitySupported() bool {
	return false
}

func sameDeviceRootPair(_ Root, _ Root, _, _ fs.FileInfo) bool {
	return false
}
