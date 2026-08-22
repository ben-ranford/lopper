//go:build windows

package safeio

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

const (
	windowsDeleteAccess              = 0x00010000
	windowsSynchronize               = 0x00100000
	windowsFileOpenReparsePoint      = 0x00200000
	windowsFileOpenForBackupIntent   = 0x00004000
	windowsFileSynchronousIONonalert = 0x00000020
	windowsFileRenamePosixSemantics  = 0x00000002
	windowsFileRenameInformationID   = 10
	windowsFileRenameInformationExID = 65
	windowsObjCaseInsensitive        = 0x00000040
)

var (
	windowsNTDLL                          = syscall.NewLazyDLL("ntdll.dll")
	windowsProcNtOpenFile                 = windowsNTDLL.NewProc("NtOpenFile")
	windowsProcNtSetInformationFile       = windowsNTDLL.NewProc("NtSetInformationFile")
	windowsProcRtlNtStatusToDosErrorNoTeb = windowsNTDLL.NewProc("RtlNtStatusToDosErrorNoTeb")
)

type windowsNTStatus uint32

func (s windowsNTStatus) Error() string {
	return s.Errno().Error()
}

func (s windowsNTStatus) Errno() syscall.Errno {
	ret, _, _ := syscall.SyscallN(windowsProcRtlNtStatusToDosErrorNoTeb.Addr(), uintptr(s))
	return syscall.Errno(ret)
}

type windowsIOStatusBlock struct {
	Status      windowsNTStatus
	Information uintptr
}

type windowsNTUnicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type windowsObjectAttributes struct {
	Length             uint32
	RootDirectory      syscall.Handle
	ObjectName         *windowsNTUnicodeString
	Attributes         uint32
	SecurityDescriptor uintptr
	SecurityQoS        uintptr
}

type windowsFileRenameInformation struct {
	ReplaceIfExists byte
	RootDirectory   syscall.Handle
	FileNameLength  uint32
	FileName        [syscall.MAX_PATH]uint16
}

type windowsFileRenameInformationEx struct {
	Flags          uint32
	RootDirectory  syscall.Handle
	FileNameLength uint32
	FileName       [syscall.MAX_PATH]uint16
}

func renameNoReplaceBetweenRoots(oldRoot, newRoot *osRoot, oldName, newName string) (returnErr error) {
	oldDir, err := oldRoot.root.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, oldDir.Close())
	}()

	newDir, err := newRoot.root.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, newDir.Close())
	}()

	err = renameNoReplaceAt(syscall.Handle(oldDir.Fd()), oldName, syscall.Handle(newDir.Fd()), newName)
	if err != nil {
		return &os.LinkError{Op: "rename_noreplace", Old: oldName, New: newName, Err: err}
	}
	return nil
}

func renameNoReplaceAt(oldParent syscall.Handle, oldName string, newParent syscall.Handle, newName string) (returnErr error) {
	objectAttrs, err := newWindowsObjectAttributes(oldParent, oldName)
	if err != nil {
		return err
	}

	var source syscall.Handle
	err = ntOpenFile(
		&source,
		windowsSynchronize|windowsDeleteAccess,
		&objectAttrs,
		&windowsIOStatusBlock{},
		syscall.FILE_SHARE_DELETE|syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		windowsFileOpenReparsePoint|windowsFileOpenForBackupIntent|windowsFileSynchronousIONonalert,
	)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, syscall.CloseHandle(source))
	}()

	newPath, err := syscall.UTF16FromString(newName)
	if err != nil {
		return err
	}
	if len(newPath) > len(windowsFileRenameInformationEx{}.FileName) {
		return syscall.EINVAL
	}

	renameInfoEx := windowsFileRenameInformationEx{
		Flags:         windowsFileRenamePosixSemantics,
		RootDirectory: newParent,
	}
	copy(renameInfoEx.FileName[:], newPath)
	renameInfoEx.FileNameLength = uint32((len(newPath) - 1) * 2)
	err = ntSetInformationFile(
		source,
		&windowsIOStatusBlock{},
		unsafe.Pointer(&renameInfoEx),
		uint32(unsafe.Sizeof(windowsFileRenameInformationEx{})),
		windowsFileRenameInformationExID,
	)
	if err == nil {
		return nil
	}

	renameInfo := windowsFileRenameInformation{
		RootDirectory: newParent,
	}
	copy(renameInfo.FileName[:], newPath)
	renameInfo.FileNameLength = renameInfoEx.FileNameLength
	return ntSetInformationFile(
		source,
		&windowsIOStatusBlock{},
		unsafe.Pointer(&renameInfo),
		uint32(unsafe.Sizeof(windowsFileRenameInformation{})),
		windowsFileRenameInformationID,
	)
}

func newWindowsObjectAttributes(root syscall.Handle, name string) (windowsObjectAttributes, error) {
	if name == "." {
		name = ""
	}
	p16, err := syscall.UTF16FromString(name)
	if err != nil {
		return windowsObjectAttributes{}, err
	}
	objectName := &windowsNTUnicodeString{
		Length:        uint16((len(p16) - 1) * 2),
		MaximumLength: uint16(len(p16) * 2),
		Buffer:        &p16[0],
	}
	return windowsObjectAttributes{
		Length:        uint32(unsafe.Sizeof(windowsObjectAttributes{})),
		RootDirectory: root,
		ObjectName:    objectName,
		Attributes:    windowsObjCaseInsensitive,
	}, nil
}

func ntOpenFile(handle *syscall.Handle, access uint32, objectAttrs *windowsObjectAttributes, ioStatus *windowsIOStatusBlock, share uint32, options uint32) error {
	ret, _, _ := syscall.SyscallN(
		windowsProcNtOpenFile.Addr(),
		uintptr(unsafe.Pointer(handle)),
		uintptr(access),
		uintptr(unsafe.Pointer(objectAttrs)),
		uintptr(unsafe.Pointer(ioStatus)),
		uintptr(share),
		uintptr(options),
	)
	return windowsNTStatusError(ret)
}

func ntSetInformationFile(handle syscall.Handle, ioStatus *windowsIOStatusBlock, inBuffer unsafe.Pointer, inBufferLen uint32, class uint32) error {
	ret, _, _ := syscall.SyscallN(
		windowsProcNtSetInformationFile.Addr(),
		uintptr(handle),
		uintptr(unsafe.Pointer(ioStatus)),
		uintptr(inBuffer),
		uintptr(inBufferLen),
		uintptr(class),
	)
	return windowsNTStatusError(ret)
}

func windowsNTStatusError(status uintptr) error {
	if status == 0 {
		return nil
	}
	return windowsNTStatus(status).Errno()
}
