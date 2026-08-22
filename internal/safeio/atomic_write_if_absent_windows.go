//go:build windows

package safeio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var windowsNoReplaceRenameFn = windowsNoReplaceRename

func fallbackAtomicIfAbsent(root Root, tempRel, targetRel string, tempInfo fs.FileInfo, linkErr error) error {
	if !windowsHardLinkUnsupported(linkErr, tempRel, targetRel) {
		return linkErr
	}
	named, ok := root.(namedRoot)
	if !ok {
		return linkErr
	}
	rootInfo, err := root.Lstat(".")
	if err != nil {
		return errors.Join(linkErr, err)
	}
	if err := windowsNoReplaceRenameFn(named.rootName(), rootInfo, tempRel, targetRel, tempInfo); err != nil {
		return errors.Join(linkErr, err)
	}
	return nil
}

func windowsHardLinkUnsupported(err error, oldName, newName string) bool {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) ||
		linkErr.Op != "linkat" ||
		linkErr.Old != oldName ||
		linkErr.New != newName {
		return false
	}
	return errors.Is(linkErr.Err, errors.ErrUnsupported) ||
		errors.Is(linkErr.Err, syscall.EWINDOWS) ||
		errors.Is(linkErr.Err, syscall.ERROR_PRIVILEGE_NOT_HELD) ||
		errors.Is(linkErr.Err, syscall.Errno(1))
}

func windowsNoReplaceRename(rootName string, rootInfo fs.FileInfo, tempRel, targetRel string, tempInfo fs.FileInfo) (returnErr error) {
	if tempInfo == nil {
		return fmt.Errorf("temporary file info unavailable before no-replace publish: %s", targetRel)
	}
	if filepath.Base(targetRel) != targetRel || filepath.Base(tempRel) != tempRel {
		return fmt.Errorf("windows no-replace publish requires parent-relative names: %s -> %s", tempRel, targetRel)
	}

	parentFile, err := openWindowsDirectory(rootName)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, parentFile.Close())
	}()

	parentInfo, err := parentFile.Stat()
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() || !os.SameFile(rootInfo, parentInfo) {
		return fmt.Errorf("parent root changed before no-replace publish: %s", targetRel)
	}

	tempFile, err := openWindowsFileNoFollow(filepath.Join(rootName, tempRel))
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, tempFile.Close())
	}()

	openedTempInfo, err := tempFile.Stat()
	if err != nil {
		return err
	}
	if !openedTempInfo.Mode().IsRegular() || !os.SameFile(tempInfo, openedTempInfo) {
		return fmt.Errorf("temporary file changed before no-replace publish: %s", targetRel)
	}

	return ntRenameNoReplace(syscall.Handle(tempFile.Fd()), syscall.Handle(parentFile.Fd()), targetRel)
}

func openWindowsDirectory(path string) (*os.File, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pathp,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func openWindowsFileNoFollow(path string) (*os.File, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pathp,
		windowsDeleteAccess|syscall.SYNCHRONIZE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

type ioStatusBlock struct {
	status, information uintptr
}

type fileRenameInformation struct {
	replaceIfExists byte
	rootDirectory   syscall.Handle
	fileNameLength  uint32
	fileName        [syscall.MAX_PATH]uint16
}

var procNtSetInformationFile = syscall.NewLazyDLL("ntdll.dll").NewProc("NtSetInformationFile")
var procRtlNtStatusToDosError = syscall.NewLazyDLL("ntdll.dll").NewProc("RtlNtStatusToDosError")

const fileRenameInformationClass = 10
const windowsDeleteAccess = 0x00010000

func ntRenameNoReplace(source, targetRoot syscall.Handle, targetRel string) error {
	p16, err := syscall.UTF16FromString(targetRel)
	if err != nil {
		return err
	}
	if len(p16) > len(fileRenameInformation{}.fileName) {
		return syscall.EINVAL
	}
	info := fileRenameInformation{
		rootDirectory:  targetRoot,
		fileNameLength: uint32((len(p16) - 1) * 2),
	}
	copy(info.fileName[:], p16)
	status, _, _ := procNtSetInformationFile.Call(
		uintptr(source),
		uintptr(unsafe.Pointer(&ioStatusBlock{})),
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		fileRenameInformationClass,
	)
	if status != 0 {
		err := ntStatusError(status)
		if errors.Is(err, os.ErrExist) {
			return os.ErrExist
		}
		return err
	}
	return nil
}

func ntStatusError(status uintptr) error {
	errno, _, _ := procRtlNtStatusToDosError.Call(status)
	if errno == 0 {
		return syscall.Errno(status)
	}
	return syscall.Errno(errno)
}
