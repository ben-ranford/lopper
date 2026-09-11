//go:build linux

package safeio

import "golang.org/x/sys/unix"

func renameNoReplaceBetweenRoots(oldRoot, newRoot *osRoot, oldName, newName string) error {
	return renameNoReplaceBetweenRootsSyscall(oldRoot, newRoot, oldName, newName, "renameat2", func(oldFD int, oldName string, newFD int, newName string) error {
		return unix.Renameat2(oldFD, oldName, newFD, newName, unix.RENAME_NOREPLACE)
	})
}
