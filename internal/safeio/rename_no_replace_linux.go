//go:build linux

package safeio

import "golang.org/x/sys/unix"

const renameNoReplaceOp = "renameat2"

func renameNoReplaceAt(oldFD int, oldName string, newFD int, newName string) error {
	return unix.Renameat2(oldFD, oldName, newFD, newName, unix.RENAME_NOREPLACE)
}
