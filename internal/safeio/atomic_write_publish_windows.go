//go:build windows

package safeio

import (
	"errors"
	"os"
	"syscall"
)

const (
	windowsErrorInvalidFunction syscall.Errno = 1
	windowsErrorNotSupported    syscall.Errno = 50
)

// publishAtomicFileIfAbsent uses a hard link when available. Windows rename
// rejects an existing destination, so it is a no-replace fallback for volumes
// that support atomic rename but not hard links (such as FAT-derived shares).
func publishAtomicFileIfAbsent(root Root, tempRel, targetRel string) (bool, error) {
	linkErr := root.Link(tempRel, targetRel)
	if linkErr == nil {
		return false, nil
	}
	if !windowsHardLinkUnsupported(linkErr, tempRel, targetRel) {
		return false, linkErr
	}
	if err := root.Rename(tempRel, targetRel); err != nil {
		return false, errors.Join(linkErr, err)
	}
	return true, nil
}

func windowsHardLinkUnsupported(err error, oldName, newName string) bool {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || linkErr.Op != "link" || linkErr.Old != oldName || linkErr.New != newName {
		return false
	}
	errno, ok := linkErr.Err.(syscall.Errno)
	return ok && (errno == windowsErrorNotSupported || errno == windowsErrorInvalidFunction)
}
