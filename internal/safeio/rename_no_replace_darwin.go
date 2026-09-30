//go:build darwin

package safeio

import "golang.org/x/sys/unix"

const renameNoReplaceOp = "renameatx_np"

func renameNoReplaceAt(oldFD int, oldName string, newFD int, newName string) error {
	return unix.RenameatxNp(oldFD, oldName, newFD, newName, unix.RENAME_EXCL)
}
