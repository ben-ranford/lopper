package runtime

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
)

func statRuntimeSearchDir(dir string) (os.FileInfo, error) {
	if !strings.HasPrefix(dir, `\\?\`) {
		return fs.Stat(os.DirFS(dir), ".")
	}
	// DirFS.Stat(".") appends a literal dot that extended Windows paths do not
	// normalize. Query the selected directory itself without requesting read
	// access, following directory links as the existing metadata policy does.
	name, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return nil, err
	}
	share := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE)
	handle, err := syscall.CreateFile(name, 0, share, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), dir)
	info, statErr := file.Stat()
	return info, errors.Join(statErr, file.Close())
}
