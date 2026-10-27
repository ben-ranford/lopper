//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package ui

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func validateStaveCancelReaderFile(file *os.File) error {
	if file.Name() != "/dev/tty" || file.Fd() < uintptr(unix.FD_SETSIZE) {
		return nil
	}
	return fmt.Errorf("unsupported Stave terminal input %q at descriptor %d: /dev/tty cancellation uses select and requires a descriptor below FD_SETSIZE (%d)", file.Name(), file.Fd(), unix.FD_SETSIZE)
}
