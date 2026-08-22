//go:build darwin

package safeio

const (
	sysRenameatxNP = 488
	renameExcl     = 0x00000004
)

func renameNoReplaceInRoot(root *osRoot, oldName, newName string) error {
	return renameNoReplaceInRootSyscall(root, oldName, newName, "renameatx_np", sysRenameatxNP, renameExcl)
}
