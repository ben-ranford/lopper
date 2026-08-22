//go:build !windows

package safeio

func fallbackAtomicReplacement(_ Root, _ string, _ string, _ File, _ []byte, renameErr error, _ bool) error {
	return renameErr
}
