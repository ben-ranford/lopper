//go:build !darwin && !linux && !windows

package safeio

import (
	"io/fs"
	"os"
)

func renameNoReplaceBetweenRoots(_, _ *osRoot, oldName, newName string) error {
	return &os.LinkError{Op: "rename_noreplace", Old: oldName, New: newName, Err: fs.ErrInvalid}
}
