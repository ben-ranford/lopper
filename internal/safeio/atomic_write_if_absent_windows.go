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
	rootInfo, err := root.Lstat(".")
	if err != nil {
		return errors.Join(linkErr, err)
	}
	if err := windowsNoReplaceRenameFn(root, rootInfo, tempRel, targetRel, tempInfo); err != nil {
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

func windowsNoReplaceRename(root Root, rootInfo fs.FileInfo, tempRel, targetRel string, tempInfo fs.FileInfo) (returnErr error) {
	if tempInfo == nil {
		return fmt.Errorf("temporary file info unavailable before no-replace publish: %s", targetRel)
	}
	if filepath.Base(targetRel) != targetRel || filepath.Base(tempRel) != tempRel {
		return fmt.Errorf("windows no-replace publish requires parent-relative names: %s -> %s", tempRel, targetRel)
	}

	parentFile, err := openWindowsRootDirectory(root)
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

	tempFile, err := openWindowsFileNoFollow(syscall.Handle(parentFile.Fd()), tempRel)
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

type windowsHandleFile interface {
	File
	Fd() uintptr
}

func openWindowsRootDirectory(root Root) (windowsHandleFile, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	handleFile, ok := file.(windowsHandleFile)
	if !ok {
		return nil, closeFileWithError(file, fmt.Errorf("windows no-replace root handle unavailable"))
	}
	return handleFile, nil
}

func openWindowsFileNoFollow(root syscall.Handle, name string) (*os.File, error) {
	objAttrs, err := newWindowsObjectAttributes(root, name)
	if err != nil {
		return nil, err
	}
	var handle syscall.Handle
	status, _, _ := procNtOpenFile.Call(
		uintptr(unsafe.Pointer(&handle)),
		windowsDeleteAccess|syscall.SYNCHRONIZE,
		uintptr(unsafe.Pointer(objAttrs)),
		uintptr(unsafe.Pointer(&ioStatusBlock{})),
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|windowsFileShareDelete,
		windowsFileOpenReparsePoint|windowsFileOpenForBackupIntent|windowsFileSynchronousIONonAlert,
	)
	if status != 0 {
		return nil, ntStatusError(status)
	}
	return os.NewFile(uintptr(handle), name), nil
}

type ioStatusBlock struct {
	status, information uintptr
}

type ntUnicodeString struct {
	length        uint16
	maximumLength uint16
	buffer        *uint16
}

type objectAttributes struct {
	length             uint32
	rootDirectory      syscall.Handle
	objectName         *ntUnicodeString
	attributes         uint32
	securityDescriptor uintptr
	securityQoS        uintptr
}

type fileRenameInformation struct {
	replaceIfExists byte
	rootDirectory   syscall.Handle
	fileNameLength  uint32
	fileName        [1]uint16
}

var procNtOpenFile = syscall.NewLazyDLL("ntdll.dll").NewProc("NtOpenFile")
var procNtSetInformationFile = syscall.NewLazyDLL("ntdll.dll").NewProc("NtSetInformationFile")
var procRtlNtStatusToDosError = syscall.NewLazyDLL("ntdll.dll").NewProc("RtlNtStatusToDosError")

const fileRenameInformationClass = 10
const windowsDeleteAccess = 0x00010000
const maxWindowsRenameTargetUTF16 = 32767
const windowsFileShareDelete = 0x00000004
const windowsFileOpenForBackupIntent = 0x00004000
const windowsFileSynchronousIONonAlert = 0x00000020
const windowsFileOpenReparsePoint = 0x00200000

func newWindowsObjectAttributes(root syscall.Handle, name string) (*objectAttributes, error) {
	objectName, err := newNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	return &objectAttributes{
		length:        uint32(unsafe.Sizeof(objectAttributes{})),
		rootDirectory: root,
		objectName:    objectName,
	}, nil
}

func newNTUnicodeString(name string) (*ntUnicodeString, error) {
	p16, err := syscall.UTF16FromString(name)
	if err != nil {
		return nil, err
	}
	byteLength := len(p16) * 2
	if byteLength > (1<<16)-1 {
		return nil, syscall.EINVAL
	}
	return &ntUnicodeString{
		length:        uint16(byteLength - 2),
		maximumLength: uint16(byteLength),
		buffer:        &p16[0],
	}, nil
}

func ntRenameNoReplace(source, targetRoot syscall.Handle, targetRel string) error {
	renameInfo, err := newFileRenameInformation(targetRoot, targetRel)
	if err != nil {
		return err
	}
	status, _, _ := procNtSetInformationFile.Call(
		uintptr(source),
		uintptr(unsafe.Pointer(&ioStatusBlock{})),
		uintptr(unsafe.Pointer(&renameInfo[0])),
		uintptr(len(renameInfo)),
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

func newFileRenameInformation(targetRoot syscall.Handle, targetRel string) ([]byte, error) {
	p16, err := syscall.UTF16FromString(targetRel)
	if err != nil {
		return nil, err
	}
	nameLength := len(p16) - 1
	if nameLength == 0 {
		return nil, syscall.EINVAL
	}
	if nameLength > maxWindowsRenameTargetUTF16 {
		return nil, syscall.Errno(206)
	}
	fileNameOffset := unsafe.Offsetof(fileRenameInformation{}.fileName)
	fileNameBytes := nameLength * 2
	renameInfo := make([]byte, int(fileNameOffset)+fileNameBytes)
	info := (*fileRenameInformation)(unsafe.Pointer(&renameInfo[0]))
	info.rootDirectory = targetRoot
	info.fileNameLength = uint32(fileNameBytes)
	fileName := unsafe.Slice(&info.fileName[0], nameLength)
	copy(fileName, p16[:nameLength])
	return renameInfo, nil
}

func fileRenameInformationView(buffer []byte) *fileRenameInformation {
	if len(buffer) == 0 {
		return nil
	}
	return (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
}

func ntStatusError(status uintptr) error {
	errno, _, _ := procRtlNtStatusToDosError.Call(status)
	if errno == 0 {
		return syscall.Errno(status)
	}
	return syscall.Errno(errno)
}
