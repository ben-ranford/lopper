//go:build !windows

package safeio

import "io/fs"

func createPrivateAtomicTempFile(root Root, dir string) (string, File, error) {
	return createAtomicTempFile(root, dir, 0o600)
}

func filePrivateToOwner(_ File, info fs.FileInfo) (bool, error) {
	return info.Mode().Perm()&0o077 == 0, nil
}
