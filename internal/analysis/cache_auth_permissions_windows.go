//go:build windows

package analysis

import "io/fs"

func authKeyFileModeTooPermissive(fs.FileMode) bool {
	// Windows ACLs are not represented in os.FileMode permission bits, and
	// writable files commonly report 0666 even when protected by user ACLs.
	return false
}
