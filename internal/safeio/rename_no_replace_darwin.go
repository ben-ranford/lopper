//go:build darwin

package safeio

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	sysRenameatxNP = 488
	renameExcl     = 0x00000004
)

func renameNoReplaceInRoot(root *osRoot, oldName, newName string) (returnErr error) {
	dir, err := root.root.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, dir.Close())
	}()

	oldPtr, err := syscall.BytePtrFromString(oldName)
	if err != nil {
		return err
	}
	newPtr, err := syscall.BytePtrFromString(newName)
	if err != nil {
		return err
	}
	conn, err := dir.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := conn.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall6(sysRenameatxNP, fd, uintptr(unsafe.Pointer(oldPtr)), fd, uintptr(unsafe.Pointer(newPtr)), renameExcl, 0)
	}); err != nil {
		return err
	}
	if errno != 0 {
		return &os.LinkError{Op: "renameatx_np", Old: oldName, New: newName, Err: errno}
	}
	return nil
}
