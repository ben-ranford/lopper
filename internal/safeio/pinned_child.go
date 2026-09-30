package safeio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// childRootOperations is the common contract of confined roots and adapter
// roots that expose a narrower directory-only interface.
type childRootOperations[T any] interface {
	Lstat(string) (fs.FileInfo, error)
	OpenRoot(string) (T, error)
	Close() error
}

// OpenPinnedChildRoot opens a directory only if its entry is not a symlink and
// the opened root still identifies that entry. The caller owns a successful
// result; failures close an acquired child and retain any close error.
// changedMessage preserves the caller's diagnostic for an identity mismatch.
func OpenPinnedChildRoot[T childRootOperations[T]](root T, name, path, changedMessage string) (T, error) {
	return openValidatedRoot(root, name, path, func() (fs.FileInfo, error) { return root.Lstat(name) }, "root contains symlink", "root is not a directory", changedMessage, func(child T, err error) error { return errors.Join(err, child.Close()) })
}

func openValidatedRoot[T childRootOperations[T]](root T, name, path string, infoFn func() (fs.FileInfo, error), symlinkMessage, notDirMessage, changedMessage string, closeWithError func(T, error) error) (T, error) {
	var zero T
	info, err := infoFn()
	if err != nil {
		return zero, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return zero, fmt.Errorf("%s: %s", symlinkMessage, path)
	}
	if !info.IsDir() {
		return zero, fmt.Errorf("%s: %s", notDirMessage, path)
	}

	child, err := root.OpenRoot(name)
	if err != nil {
		return zero, err
	}
	openedInfo, err := child.Lstat(".")
	if err != nil {
		return zero, closeWithError(child, err)
	}
	if !os.SameFile(info, openedInfo) {
		return zero, closeWithError(child, fmt.Errorf("%s: %s", changedMessage, path))
	}
	return child, nil
}
