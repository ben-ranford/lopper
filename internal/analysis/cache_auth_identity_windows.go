//go:build windows

package analysis

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/ben-ranford/lopper/internal/safeio"
)

var analysisCacheOpenStorageIdentityFileFn = openStorageIdentityFile

func storageDirectoryIdentity(storageRoot string, info fs.FileInfo) (identity string, returnErr error) {
	if info == nil || !info.IsDir() {
		return "", fmt.Errorf("storage root is not a directory")
	}
	file, err := analysisCacheOpenStorageIdentityFileFn(storageRoot)
	if err != nil {
		return "", fmt.Errorf("reopen cache storage directory: %w", err)
	}
	defer func() {
		closeErr := file.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close reopened cache storage directory: %w", closeErr)
		}
		returnErr = errors.Join(returnErr, closeErr)
	}()

	openedInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat reopened cache storage directory: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return "", fmt.Errorf(
			"%w: cache storage directory changed while resolving auth identity: %s",
			safeio.ErrFileChanged,
			storageRoot,
		)
	}

	var fileInfo syscall.ByHandleFileInformation
	handle := syscall.Handle(file.Fd())
	if err := syscall.GetFileInformationByHandle(handle, &fileInfo); err != nil {
		return "", fmt.Errorf("read cache storage directory identity: %w", err)
	}
	return fmt.Sprintf(
		"volume:%08x;file:%08x%08x",
		fileInfo.VolumeSerialNumber,
		fileInfo.FileIndexHigh,
		fileInfo.FileIndexLow,
	), nil
}

func openStorageIdentityFile(storageRoot string) (*os.File, error) {
	path, err := syscall.UTF16PtrFromString(storageRoot)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		path,
		0,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), storageRoot)
	if file == nil {
		closeErr := syscall.CloseHandle(handle)
		return nil, errors.Join(
			fmt.Errorf("wrap cache storage directory handle: %s", storageRoot),
			closeErr,
		)
	}
	return file, nil
}
