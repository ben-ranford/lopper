//go:build windows

package safeio

import "io/fs"

func openPinnedReplacementTargetIfNeeded(root Root, targetRel string, expectedInfo fs.FileInfo) (File, func() error, error) {
	if expectedInfo == nil {
		return nil, func() error { return nil }, nil
	}
	file, err := openPinnedReplacementTarget(root, targetRel, expectedInfo)
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}
