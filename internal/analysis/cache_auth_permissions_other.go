//go:build !windows

package analysis

import "io/fs"

func authKeyFileModeTooPermissive(mode fs.FileMode) bool {
	return mode.Perm()&0o077 != 0
}
