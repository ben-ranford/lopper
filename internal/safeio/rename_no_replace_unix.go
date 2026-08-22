//go:build darwin || linux

package safeio

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

func renameNoReplaceBetweenRootsSyscall(oldRoot, newRoot *osRoot, oldName, newName, op string, sysno, flags uintptr) (returnErr error) {
	oldDir, err := oldRoot.root.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, oldDir.Close())
	}()

	newDir, err := newRoot.root.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, newDir.Close())
	}()

	oldPtr, err := syscall.BytePtrFromString(oldName)
	if err != nil {
		return err
	}
	newPtr, err := syscall.BytePtrFromString(newName)
	if err != nil {
		return err
	}
	oldConn, err := oldDir.SyscallConn()
	if err != nil {
		return err
	}
	newConn, err := newDir.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	var controlErr error
	if err := oldConn.Control(func(oldFD uintptr) {
		controlErr = newConn.Control(func(newFD uintptr) {
			_, _, errno = syscall.Syscall6(sysno, oldFD, uintptr(unsafe.Pointer(oldPtr)), newFD, uintptr(unsafe.Pointer(newPtr)), flags, 0)
		})
	}); err != nil {
		return err
	}
	if controlErr != nil {
		return controlErr
	}
	if errno != 0 {
		return &os.LinkError{Op: op, Old: oldName, New: newName, Err: errno}
	}
	return nil
}
