//go:build windows

package safeio

func renameNoReplaceInRoot(root *osRoot, oldName, newName string) error {
	return root.root.Rename(oldName, newName)
}
