//go:build !windows

package runtime

import (
	"io/fs"
	"os"
)

func statRuntimeSearchDir(dir string) (os.FileInfo, error) {
	return fs.Stat(os.DirFS(dir), ".")
}
