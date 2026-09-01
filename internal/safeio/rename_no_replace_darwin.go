//go:build darwin

package safeio

import "golang.org/x/sys/unix"

func renameNoReplaceBetweenRoots(oldRoot, newRoot *osRoot, oldName, newName string) error {
	return renameNoReplaceBetweenRootsSyscall(oldRoot, newRoot, oldName, newName, "renameatx_np", func(oldFD int, oldName string, newFD int, newName string) error {
		return unix.RenameatxNp(oldFD, oldName, newFD, newName, unix.RENAME_EXCL)
	})
}
