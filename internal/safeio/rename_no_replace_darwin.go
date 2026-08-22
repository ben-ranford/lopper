//go:build darwin

package safeio

const (
	sysRenameatxNP = 488
	renameExcl     = 0x00000004
)

func renameNoReplaceBetweenRoots(oldRoot, newRoot *osRoot, oldName, newName string) error {
	return renameNoReplaceBetweenRootsSyscall(oldRoot, newRoot, oldName, newName, "renameatx_np", sysRenameatxNP, renameExcl)
}
