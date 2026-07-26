//go:build !windows

package safeio

import "io/fs"

type privateFileAccessSnapshot struct{}

func createPrivateAtomicTempFile(root Root, dir string) (string, File, error) {
	return createAtomicTempFile(root, dir, 0o600)
}

func filePrivateToOwner(_ File, info fs.FileInfo) (bool, error) {
	return info.Mode().Perm()&0o077 == 0, nil
}

func capturePrivateFileAccessSnapshot(file File, info fs.FileInfo) (privateFileAccessSnapshot, bool, error) {
	private, err := filePrivateToOwner(file, info)
	return privateFileAccessSnapshot{}, private, err
}

func samePrivateFileAccessSnapshot(privateFileAccessSnapshot, privateFileAccessSnapshot) bool {
	return true
}
