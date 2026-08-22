//go:build darwin || linux

package safeio

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

func renameNoReplaceInRootSyscall(root *osRoot, oldName, newName, op string, sysno, flags uintptr) (returnErr error) {
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
		_, _, errno = syscall.Syscall6(sysno, fd, uintptr(unsafe.Pointer(oldPtr)), fd, uintptr(unsafe.Pointer(newPtr)), flags, 0)
	}); err != nil {
		return err
	}
	if errno != 0 {
		return &os.LinkError{Op: op, Old: oldName, New: newName, Err: errno}
	}
	return nil
}
