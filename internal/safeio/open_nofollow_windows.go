//go:build windows

package safeio

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const windowsNoFollowCreateOptions = windows.FILE_SYNCHRONOUS_IO_NONALERT |
	windows.FILE_NON_DIRECTORY_FILE |
	windows.FILE_OPEN_REPARSE_POINT

type windowsNTCreateFileRequest struct {
	handle           *windows.Handle
	access           uint32
	objectAttributes *windows.OBJECT_ATTRIBUTES
	ioStatusBlock    *windows.IO_STATUS_BLOCK
	allocationSize   *int64
	attributes       uint32
	share            uint32
	disposition      uint32
	options          uint32
	eaBuffer         uintptr
	eaLength         uint32
}

var windowsNtCreateFile = func(request windowsNTCreateFileRequest) error {
	return windows.NtCreateFile(
		request.handle,
		request.access,
		request.objectAttributes,
		request.ioStatusBlock,
		request.allocationSize,
		request.attributes,
		request.share,
		request.disposition,
		request.options,
		request.eaBuffer,
		request.eaLength,
	)
}

func openRootFileNoFollow(root *os.Root, name string) (*os.File, error) {
	return openRootFileNoFollowAtomic(root, name, openWindowsRootFileNoFollow)
}

func openWindowsRootFileNoFollow(root *os.Root, name string) (*os.File, error) {
	rootHandle, err := osRootHandle(root)
	if err != nil {
		return nil, fmt.Errorf("%w on windows: root handle extraction is unavailable: %w", ErrOpenFileNoFollowUnsupported, err)
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{
		RootDirectory: rootHandle,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	attributes.Length = uint32(unsafe.Sizeof(attributes))

	var handle windows.Handle
	err = windowsNtCreateFile(windowsNTCreateFileRequest{
		handle:           &handle,
		access:           windows.FILE_GENERIC_READ,
		objectAttributes: &attributes,
		ioStatusBlock:    &windows.IO_STATUS_BLOCK{},
		attributes:       windows.FILE_ATTRIBUTE_NORMAL,
		share:            windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE,
		disposition:      windows.FILE_OPEN,
		options:          windowsNoFollowCreateOptions,
	})
	runtime.KeepAlive(root)
	if err != nil {
		return nil, joinWindowsNTStatusErrno(err)
	}
	if err := validateWindowsNoFollowHandle(handle, name); err != nil {
		return nil, closeWindowsNoFollowHandleWithError(handle, err)
	}

	file := os.NewFile(uintptr(handle), name)
	if file == nil {
		return nil, closeWindowsNoFollowHandleWithError(handle, fmt.Errorf("NtCreateFile %s: failed to wrap handle", name))
	}
	return file, nil
}

func osRootHandle(root *os.Root) (windows.Handle, error) {
	descriptor, err := osRootDescriptorField(root)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if descriptor.Kind() != reflect.Uintptr {
		return windows.InvalidHandle, fmt.Errorf("no-follow file open unsupported on windows: root handle unavailable")
	}
	return windows.Handle(descriptor.Uint()), nil
}

func validateWindowsNoFollowHandle(handle windows.Handle, name string) error {
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		return &os.PathError{Op: "GetFileType", Path: name, Err: err}
	}
	if fileType != windows.FILE_TYPE_DISK {
		return &os.PathError{Op: "GetFileType", Path: name, Err: ErrNoFollowFinalComponent}
	}

	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return &os.PathError{Op: "GetFileInformationByHandle", Path: name, Err: err}
	}
	rejectedAttributes := uint32(
		windows.FILE_ATTRIBUTE_REPARSE_POINT |
			windows.FILE_ATTRIBUTE_DIRECTORY |
			windows.FILE_ATTRIBUTE_DEVICE,
	)
	if info.FileAttributes&rejectedAttributes != 0 {
		return &os.PathError{Op: "GetFileInformationByHandle", Path: name, Err: ErrNoFollowFinalComponent}
	}
	return nil
}

func closeWindowsNoFollowHandleWithError(handle windows.Handle, err error) error {
	return errors.Join(err, windows.CloseHandle(handle))
}

func joinWindowsNTStatusErrno(err error) error {
	var status windows.NTStatus
	if !errors.As(err, &status) {
		return err
	}
	errno := status.Errno()
	if errno == 0 {
		return err
	}
	return errors.Join(err, errno)
}

func isAtomicNoFollowLeafError(err error) bool {
	return errorsIsAny(
		err,
		syscall.ELOOP,
		syscall.ENOTDIR,
		syscall.EISDIR,
		windows.STATUS_FILE_IS_A_DIRECTORY,
		windows.STATUS_NOT_A_DIRECTORY,
		windows.STATUS_REPARSE_POINT_ENCOUNTERED,
		windows.STATUS_REPARSE_POINT_NOT_RESOLVED,
		windows.STATUS_DIRECTORY_IS_A_REPARSE_POINT,
	)
}
