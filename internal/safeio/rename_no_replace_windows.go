//go:build windows

package safeio

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	win "golang.org/x/sys/windows"
)

const (
	windowsFileRenameInformationID   = 10
	windowsFileRenameInformationExID = 65
)

type windowsFileRenameInformation struct {
	ReplaceIfExists byte
	RootDirectory   win.Handle
	FileNameLength  uint32
	FileName        [win.MAX_LONG_PATH]uint16
}

type windowsFileRenameInformationEx struct {
	Flags          uint32
	RootDirectory  win.Handle
	FileNameLength uint32
	FileName       [win.MAX_LONG_PATH]uint16
}

func renameNoReplaceBetweenRoots(oldRoot, newRoot *osRoot, oldName, newName string) (returnErr error) {
	oldDir, err := oldRoot.root.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := oldDir.Close(); returnErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	newDir, err := newRoot.root.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := newDir.Close(); returnErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	err = renameNoReplaceAt(win.Handle(oldDir.Fd()), oldName, win.Handle(newDir.Fd()), newName)
	if err != nil {
		return &os.LinkError{Op: "rename_noreplace", Old: oldName, New: newName, Err: err}
	}
	return nil
}

func renameNoReplaceAt(oldParent win.Handle, oldName string, newParent win.Handle, newName string) (returnErr error) {
	objectAttrs, err := newRenameNoReplaceWindowsObjectAttributes(oldParent, oldName)
	if err != nil {
		return err
	}

	var source win.Handle
	var ioStatus win.IO_STATUS_BLOCK
	var allocationSize int64
	err = win.NtCreateFile(
		&source,
		win.SYNCHRONIZE|win.DELETE,
		&objectAttrs,
		&ioStatus,
		&allocationSize,
		0,
		win.FILE_SHARE_DELETE|win.FILE_SHARE_READ|win.FILE_SHARE_WRITE,
		win.FILE_OPEN,
		win.FILE_OPEN_REPARSE_POINT|win.FILE_OPEN_FOR_BACKUP_INTENT|win.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := win.CloseHandle(source); returnErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	newPath, err := win.UTF16FromString(newName)
	if err != nil {
		return err
	}
	if len(newPath) > len(windowsFileRenameInformationEx{}.FileName) {
		return syscall.EINVAL
	}

	renameInfoEx := windowsFileRenameInformationEx{
		Flags:         win.FILE_RENAME_POSIX_SEMANTICS,
		RootDirectory: newParent,
	}
	copy(renameInfoEx.FileName[:], newPath)
	renameInfoEx.FileNameLength = uint32((len(newPath) - 1) * 2)
	err = win.NtSetInformationFile(
		source,
		&win.IO_STATUS_BLOCK{},
		(*byte)(unsafe.Pointer(&renameInfoEx)),
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
	return win.NtSetInformationFile(
		source,
		&win.IO_STATUS_BLOCK{},
		(*byte)(unsafe.Pointer(&renameInfo)),
		uint32(unsafe.Sizeof(windowsFileRenameInformation{})),
		windowsFileRenameInformationID,
	)
}

func newRenameNoReplaceWindowsObjectAttributes(root win.Handle, name string) (win.OBJECT_ATTRIBUTES, error) {
	if name == "." {
		name = ""
	}
	objectName, err := win.NewNTUnicodeString(name)
	if err != nil {
		return win.OBJECT_ATTRIBUTES{}, err
	}
	return win.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(win.OBJECT_ATTRIBUTES{})),
		RootDirectory: root,
		ObjectName:    objectName,
		Attributes:    win.OBJ_CASE_INSENSITIVE,
	}, nil
}
