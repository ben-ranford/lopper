//go:build !windows

package safeio

import "io/fs"

func fallbackAtomicIfAbsent(_ Root, _, _, _ string, _ fs.FileInfo, linkErr error) error {
	return linkErr
}
