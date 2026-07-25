//go:build windows

package safeio

import (
	"errors"
	"os"
	"syscall"
)

func windowsReplaceExistingRenameFallback(err error, oldName, newName string) bool {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) ||
		linkErr.Op != "renameat" ||
		linkErr.Old != oldName ||
		linkErr.New != newName {
		return false
	}
	errno, ok := linkErr.Err.(syscall.Errno)
	return ok &&
		(errno == syscall.ERROR_ALREADY_EXISTS || errno == syscall.ERROR_FILE_EXISTS)
}
