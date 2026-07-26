//go:build (!linux && !darwin) || !cgo

package safeio

func renameNoReplaceInDirectory(uintptr, string, string) error {
	return ErrRenameNoReplaceUnsupported
}
