//go:build !windows

package safeio

import "io/fs"

func fallbackAtomicIfAbsent(_ Root, _ string, _ string, _ fs.FileInfo, linkErr error) error {
	return linkErr
}
