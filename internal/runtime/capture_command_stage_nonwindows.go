//go:build !windows

package runtime

import (
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func openPinnedRuntimeExecutableSourceFile(root safeio.Root, name, _ string) (safeio.File, fs.FileInfo, error) {
	file, trusted := openTrustedRuntimeExecutable(root, name)
	if !trusted {
		return nil, nil, errors.New("runtime executable changed while pinning")
	}
	info, err := file.Stat()
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	return file, info, nil
}

func pinRuntimeExecutable(root safeio.Root, name, _, _ string, expected fs.FileInfo) (io.Closer, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if expected == nil || !os.SameFile(expected, info) {
		return nil, errors.Join(errors.New("staged runtime executable changed before pinning"), file.Close())
	}
	return file, nil
}
