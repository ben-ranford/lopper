//go:build !windows

package safeio

import "os"

func fallbackAtomicReplacement(_ Root, _ string, _ string, _ File, _ []byte, _ os.FileMode, _ bool, renameErr error) error {
	return renameErr
}
