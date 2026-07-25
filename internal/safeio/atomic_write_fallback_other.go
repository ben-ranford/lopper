//go:build !windows

package safeio

func fallbackAtomicReplacement(_ Root, _ string, _ string, _ File, _ []byte, renameErr error) error {
	return renameErr
}
