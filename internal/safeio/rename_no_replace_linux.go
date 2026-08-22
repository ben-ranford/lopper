//go:build linux

package safeio

const renameNoReplace = 1

func renameNoReplaceBetweenRoots(oldRoot, newRoot *osRoot, oldName, newName string) error {
	return renameNoReplaceBetweenRootsSyscall(oldRoot, newRoot, oldName, newName, "renameat2", sysRenameat2, renameNoReplace)
}
