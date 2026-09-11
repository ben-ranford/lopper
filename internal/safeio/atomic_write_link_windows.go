//go:build windows

package safeio

import (
	"errors"
	"syscall"
)

func platformIdentityBoundLinkUnsupported(err error) bool {
	return errors.Is(err, syscall.ERROR_PRIVILEGE_NOT_HELD) ||
		errors.Is(err, syscall.Errno(1))
}
