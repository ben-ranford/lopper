//go:build !windows

package safeio

import "errors"

func syncRootDirectory(root Root) (returnErr error) {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, dir.Close())
	}()
	return dir.Sync()
}
