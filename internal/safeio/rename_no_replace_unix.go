//go:build darwin || linux

package safeio

import (
	"errors"
	"os"
	"syscall"
)

type renameNoReplaceDir interface {
	Close() error
	SyscallConn() (syscall.RawConn, error)
}

type renameNoReplaceAtFunc func(oldFD int, oldName string, newFD int, newName string) error

var openRenameNoReplaceDir = func(root *osRoot) (renameNoReplaceDir, error) {
	return root.root.Open(".")
}

func renameNoReplaceBetweenRootsSyscall(oldRoot, newRoot *osRoot, oldName, newName, op string, renameAt renameNoReplaceAtFunc) (returnErr error) {
	oldDir, err := openRenameNoReplaceDir(oldRoot)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = closeRenameNoReplaceDir(returnErr, oldDir)
	}()

	newDir, err := openRenameNoReplaceDir(newRoot)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = closeRenameNoReplaceDir(returnErr, newDir)
	}()

	oldConn, err := oldDir.SyscallConn()
	if err != nil {
		return err
	}
	newConn, err := newDir.SyscallConn()
	if err != nil {
		return err
	}
	var controlErr error
	var renameErr error
	if err := oldConn.Control(func(oldFD uintptr) {
		controlErr = newConn.Control(func(newFD uintptr) {
			renameErr = renameAt(int(oldFD), oldName, int(newFD), newName)
		})
	}); err != nil {
		return err
	}
	if controlErr != nil {
		return controlErr
	}
	if renameErr != nil {
		return &os.LinkError{Op: op, Old: oldName, New: newName, Err: renameErr}
	}
	return nil
}

func closeRenameNoReplaceDir(primary error, dir renameNoReplaceDir) error {
	closeErr := dir.Close()
	if primary != nil {
		return errors.Join(primary, closeErr)
	}
	return nil
}
