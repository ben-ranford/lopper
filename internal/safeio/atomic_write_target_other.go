//go:build !windows

package safeio

import "io/fs"

func openPinnedReplacementTargetIfNeeded(Root, string, fs.FileInfo) (File, func() error, error) {
	return nil, func() error { return nil }, nil
}
