//go:build linux

package safeio

const renameNoReplace = 1

func renameNoReplaceInRoot(root *osRoot, oldName, newName string) error {
	return renameNoReplaceInRootSyscall(root, oldName, newName, "renameat2", sysRenameat2, renameNoReplace)
}
