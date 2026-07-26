//go:build windows

package safeio

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	windowsAccessAllowedACE            = 0
	windowsDACLPresent                 = 0x0004
	windowsDACLProtected               = 0x1000
	windowsDACLInformation             = 0x00000004
	windowsFileAllAccess               = 0x001f01ff
	windowsFileCreate                  = 2
	windowsFileNonDirectory            = 0x00000040
	windowsFileOpenForBackupIntent     = 0x00004000
	windowsFileOpenReparsePoint        = 0x00200000
	windowsFileSynchronousIONonAlert   = 0x00000020
	windowsInheritedACE                = 0x10
	windowsObjectCaseInsensitive       = 0x00000040
	windowsObjectDontReparse           = 0x00001000
	windowsOwnerSecurityInformation    = 0x00000001
	windowsReadControl                 = 0x00020000
	windowsSDDLRevision1               = 1
	windowsSEFileObject                = 1
	windowsStatusObjectNameCollision   = 0xc0000035
	windowsWriteDACL                   = 0x00040000
	windowsMinimumAccessAllowedACESize = 16
	windowsPrivateTempCreationAttempts = 10
)

type windowsUnicodeString struct {
	length        uint16
	maximumLength uint16
	buffer        *uint16
}

type windowsObjectAttributes struct {
	length                   uint32
	rootDirectory            syscall.Handle
	objectName               *windowsUnicodeString
	attributes               uint32
	securityDescriptor       unsafe.Pointer
	securityQualityOfService unsafe.Pointer
}

type windowsIOStatusBlock struct {
	status      uint32
	information uintptr
}

type windowsACL struct {
	revision byte
	sbz1     byte
	size     uint16
	aceCount uint16
	sbz2     uint16
}

type windowsACEHeader struct {
	aceType  byte
	aceFlags byte
	aceSize  uint16
}

type windowsAccessAllowedACEValue struct {
	header   windowsACEHeader
	mask     uint32
	sidStart uint32
}

type privateFileAccessSnapshot struct {
	owner   []byte
	dacl    []byte
	control uint16
}

var (
	windowsAdvapi32 = syscall.NewLazyDLL("advapi32.dll")
	windowsNtdll    = syscall.NewLazyDLL("ntdll.dll")

	windowsConvertStringSecurityDescriptor = windowsAdvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	windowsEqualSID                        = windowsAdvapi32.NewProc("EqualSid")
	windowsGetACE                          = windowsAdvapi32.NewProc("GetAce")
	windowsGetSecurityDescriptorControl    = windowsAdvapi32.NewProc("GetSecurityDescriptorControl")
	windowsGetSecurityInfo                 = windowsAdvapi32.NewProc("GetSecurityInfo")
	windowsIsValidSID                      = windowsAdvapi32.NewProc("IsValidSid")
	windowsNtCreateFile                    = windowsNtdll.NewProc("NtCreateFile")
	windowsRtlNtStatusToDosError           = windowsNtdll.NewProc("RtlNtStatusToDosError")
)

func createPrivateAtomicTempFile(root Root, dir string) (string, File, error) {
	tempDir, err := resolveRelativeTarget(dir, allowRootTarget)
	if err != nil {
		return "", nil, err
	}
	if tempDir != "." {
		return "", nil, fmt.Errorf("%w: private temp directory must be the pinned root", ErrPrivateFilePermissionsUnsupported)
	}

	directory, err := (&WriteRoot{root: root}).openPinnedRootDirectory()
	if err != nil {
		return "", nil, err
	}
	directoryHandle, err := windowsFileHandle(directory)
	if err != nil {
		return "", nil, errors.Join(err, directory.Close())
	}
	return createPrivateAtomicTempFileInDirectory(root, directory, directoryHandle)
}

func createPrivateAtomicTempFileInDirectory(root Root, directory File, directoryHandle syscall.Handle) (string, File, error) {
	for range windowsPrivateTempCreationAttempts {
		name, nameErr := randomTempNameFn()
		if nameErr != nil {
			return "", nil, errors.Join(nameErr, directory.Close())
		}
		file, collision, createErr := createPrivateWindowsTempCandidate(root, directoryHandle, name)
		if collision {
			continue
		}
		if createErr != nil {
			return "", nil, errors.Join(createErr, directory.Close())
		}
		if closeErr := directory.Close(); closeErr != nil {
			return "", nil, errors.Join(closeErr, cleanupAtomicTempFile(root, name, file))
		}
		return name, file, nil
	}

	return "", nil, errors.Join(fmt.Errorf("create private temp file: too many collisions"), directory.Close())
}

func createPrivateWindowsTempCandidate(root Root, directoryHandle syscall.Handle, name string) (File, bool, error) {
	handle, err := createOwnerOnlyWindowsFileAt(directoryHandle, name)
	if errors.Is(err, os.ErrExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	file := os.NewFile(uintptr(handle), name)
	if file == nil {
		return nil, false, errors.Join(
			fmt.Errorf("%w: wrap private file handle", ErrPrivateFilePermissionsUnsupported),
			syscall.CloseHandle(handle),
		)
	}
	info, statErr := file.Stat()
	private, privacyErr := filePrivateToOwner(file, info)
	validationErr := errors.Join(statErr, privacyErr)
	if validationErr == nil && private {
		return file, false, nil
	}
	if validationErr == nil {
		validationErr = fmt.Errorf("%w: created file failed owner-only validation", ErrPrivateFilePermissionsUnsupported)
	}
	return nil, false, errors.Join(validationErr, cleanupAtomicTempFile(root, name, file))
}

func createOwnerOnlyWindowsFileAt(directory syscall.Handle, name string) (handle syscall.Handle, returnErr error) {
	securityDescriptor, err := windowsOwnerOnlySecurityDescriptor()
	if err != nil {
		return syscall.InvalidHandle, err
	}
	defer func() {
		_, freeErr := syscall.LocalFree(syscall.Handle(uintptr(securityDescriptor)))
		returnErr = errors.Join(returnErr, freeErr)
		if returnErr != nil && handle != syscall.InvalidHandle {
			returnErr = errors.Join(returnErr, syscall.CloseHandle(handle))
			handle = syscall.InvalidHandle
		}
	}()

	nameUTF16, err := syscall.UTF16FromString(name)
	if err != nil {
		return syscall.InvalidHandle, err
	}
	objectName := windowsUnicodeString{
		length:        uint16((len(nameUTF16) - 1) * 2),
		maximumLength: uint16(len(nameUTF16) * 2),
		buffer:        &nameUTF16[0],
	}
	objectAttributes := windowsObjectAttributes{
		length:             uint32(unsafe.Sizeof(windowsObjectAttributes{})),
		rootDirectory:      directory,
		objectName:         &objectName,
		attributes:         windowsObjectCaseInsensitive | windowsObjectDontReparse,
		securityDescriptor: securityDescriptor,
	}
	ioStatus := windowsIOStatusBlock{}
	result, _, _ := windowsNtCreateFile.Call(
		uintptr(unsafe.Pointer(&handle)),
		uintptr(syscall.GENERIC_READ|syscall.GENERIC_WRITE|windowsReadControl|windowsWriteDACL|syscall.SYNCHRONIZE),
		uintptr(unsafe.Pointer(&objectAttributes)),
		uintptr(unsafe.Pointer(&ioStatus)),
		0,
		uintptr(syscall.FILE_ATTRIBUTE_NORMAL),
		uintptr(syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE),
		windowsFileCreate,
		windowsFileSynchronousIONonAlert|windowsFileNonDirectory|windowsFileOpenForBackupIntent|windowsFileOpenReparsePoint,
		0,
		0,
	)
	runtime.KeepAlive(nameUTF16)
	runtime.KeepAlive(securityDescriptor)
	status := uint32(result)
	if status == 0 {
		return handle, nil
	}
	handle = syscall.InvalidHandle
	if status == windowsStatusObjectNameCollision {
		return handle, os.ErrExist
	}
	return handle, windowsNTStatusError(status)
}

func windowsOwnerOnlySecurityDescriptor() (unsafe.Pointer, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return nil, fmt.Errorf("open process token for private file: %w", err)
	}
	user, userErr := token.GetTokenUser()
	closeErr := token.Close()
	if userErr != nil || closeErr != nil {
		return nil, errors.Join(userErr, closeErr)
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		return nil, fmt.Errorf("resolve process SID for private file: %w", err)
	}
	return windowsSecurityDescriptorFromSDDL(fmt.Sprintf("O:%sD:P(A;;FA;;;%s)", sid, sid))
}

func windowsSecurityDescriptorFromSDDL(sddl string) (unsafe.Pointer, error) {
	sddlUTF16, err := syscall.UTF16PtrFromString(sddl)
	if err != nil {
		return nil, err
	}
	var descriptor unsafe.Pointer
	result, _, callErr := windowsConvertStringSecurityDescriptor.Call(
		uintptr(unsafe.Pointer(sddlUTF16)),
		windowsSDDLRevision1,
		uintptr(unsafe.Pointer(&descriptor)),
		0,
	)
	runtime.KeepAlive(sddlUTF16)
	if result == 0 {
		return nil, windowsLastError("convert private file security descriptor", callErr)
	}
	return descriptor, nil
}

func filePrivateToOwner(file File, _ fs.FileInfo) (private bool, returnErr error) {
	handle, err := windowsFileHandle(file)
	if err != nil {
		return false, err
	}
	snapshot, private, err := captureWindowsPrivateFileAccessSnapshot(handle)
	_ = snapshot
	return private, err
}

func capturePrivateFileAccessSnapshot(file File, _ fs.FileInfo) (privateFileAccessSnapshot, bool, error) {
	handle, err := windowsFileHandle(file)
	if err != nil {
		return privateFileAccessSnapshot{}, false, err
	}
	return captureWindowsPrivateFileAccessSnapshot(handle)
}

func samePrivateFileAccessSnapshot(before, after privateFileAccessSnapshot) bool {
	return before.control == after.control &&
		bytes.Equal(before.owner, after.owner) &&
		bytes.Equal(before.dacl, after.dacl)
}

func captureWindowsPrivateFileAccessSnapshot(handle syscall.Handle) (snapshot privateFileAccessSnapshot, private bool, returnErr error) {
	owner, dacl, securityDescriptor, err := windowsFileSecurityInfo(handle)
	if err != nil {
		return privateFileAccessSnapshot{}, false, err
	}
	defer func() {
		_, freeErr := syscall.LocalFree(syscall.Handle(uintptr(securityDescriptor)))
		returnErr = errors.Join(returnErr, freeErr)
	}()
	control, err := windowsSecurityDescriptorControl(securityDescriptor)
	if err != nil {
		return privateFileAccessSnapshot{}, false, err
	}
	snapshot, err = windowsPrivateFileAccessSnapshot(owner, dacl, control)
	if err != nil {
		return privateFileAccessSnapshot{}, false, err
	}
	if owner == nil || dacl == nil || dacl.aceCount != 1 {
		return snapshot, false, nil
	}
	if !windowsOwnerOnlyDACLControlValue(control) {
		return snapshot, false, nil
	}
	private, err = windowsSingleOwnerACE(owner, dacl)
	return snapshot, private, err
}

func windowsFileSecurityInfo(handle syscall.Handle) (*syscall.SID, *windowsACL, unsafe.Pointer, error) {
	var (
		owner              *syscall.SID
		dacl               *windowsACL
		securityDescriptor unsafe.Pointer
	)
	result, _, _ := windowsGetSecurityInfo.Call(
		uintptr(handle),
		windowsSEFileObject,
		windowsOwnerSecurityInformation|windowsDACLInformation,
		uintptr(unsafe.Pointer(&owner)),
		0,
		uintptr(unsafe.Pointer(&dacl)),
		0,
		uintptr(unsafe.Pointer(&securityDescriptor)),
	)
	if result != 0 {
		return nil, nil, nil, fmt.Errorf("read private file security descriptor: %w", syscall.Errno(result))
	}
	return owner, dacl, securityDescriptor, nil
}

func windowsSecurityDescriptorControl(securityDescriptor unsafe.Pointer) (uint16, error) {
	var (
		control  uint16
		revision uint32
	)
	result, _, callErr := windowsGetSecurityDescriptorControl.Call(
		uintptr(securityDescriptor),
		uintptr(unsafe.Pointer(&control)),
		uintptr(unsafe.Pointer(&revision)),
	)
	if result == 0 {
		return 0, windowsLastError("read private file security descriptor control", callErr)
	}
	return control, nil
}

func windowsOwnerOnlyDACLControl(securityDescriptor unsafe.Pointer) (bool, error) {
	control, err := windowsSecurityDescriptorControl(securityDescriptor)
	if err != nil {
		return false, err
	}
	return windowsOwnerOnlyDACLControlValue(control), nil
}

func windowsOwnerOnlyDACLControlValue(control uint16) bool {
	return control&windowsDACLPresent != 0 && control&windowsDACLProtected != 0
}

func windowsSingleOwnerACE(owner *syscall.SID, dacl *windowsACL) (bool, error) {
	var acePointer unsafe.Pointer
	result, _, callErr := windowsGetACE.Call(
		uintptr(unsafe.Pointer(dacl)),
		0,
		uintptr(unsafe.Pointer(&acePointer)),
	)
	if result == 0 {
		return false, windowsLastError("read private file access entry", callErr)
	}
	ace := (*windowsAccessAllowedACEValue)(acePointer)
	if ace == nil ||
		ace.header.aceType != windowsAccessAllowedACE ||
		ace.header.aceFlags&windowsInheritedACE != 0 ||
		ace.header.aceSize < windowsMinimumAccessAllowedACESize ||
		ace.mask&windowsFileAllAccess != windowsFileAllAccess {
		return false, nil
	}
	aceSID := (*syscall.SID)(unsafe.Pointer(&ace.sidStart))
	result, _, callErr = windowsIsValidSID.Call(uintptr(unsafe.Pointer(aceSID)))
	if result == 0 {
		return false, windowsLastError("validate private file owner SID", callErr)
	}
	result, _, callErr = windowsEqualSID.Call(
		uintptr(unsafe.Pointer(owner)),
		uintptr(unsafe.Pointer(aceSID)),
	)
	if result == 0 {
		if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
			return false, fmt.Errorf("compare private file owner SID: %w", errno)
		}
		return false, nil
	}
	return true, nil
}

func windowsPrivateFileAccessSnapshot(owner *syscall.SID, dacl *windowsACL, control uint16) (privateFileAccessSnapshot, error) {
	snapshot := privateFileAccessSnapshot{control: control}
	if owner != nil {
		result, _, callErr := windowsIsValidSID.Call(uintptr(unsafe.Pointer(owner)))
		if result == 0 {
			return privateFileAccessSnapshot{}, windowsLastError("validate private file owner SID", callErr)
		}
		snapshot.owner = append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(owner)), owner.Len())...)
	}
	if dacl != nil {
		snapshot.dacl = append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(dacl)), int(dacl.size))...)
	}
	return snapshot, nil
}

func windowsFileHandle(file File) (syscall.Handle, error) {
	descriptor, ok := file.(interface{ Fd() uintptr })
	if !ok {
		return syscall.InvalidHandle, ErrPrivateFilePermissionsUnsupported
	}
	handle := syscall.Handle(descriptor.Fd())
	if handle == syscall.InvalidHandle {
		return syscall.InvalidHandle, ErrPrivateFilePermissionsUnsupported
	}
	return handle, nil
}

func windowsNTStatusError(status uint32) error {
	result, _, _ := windowsRtlNtStatusToDosError.Call(uintptr(status))
	if result == 0 || uint32(result) == ^uint32(0) {
		return fmt.Errorf("Windows NT status %#x", status)
	}
	return syscall.Errno(result)
}

func windowsLastError(operation string, callErr error) error {
	if errno, ok := callErr.(syscall.Errno); ok && errno != 0 {
		return fmt.Errorf("%s: %w", operation, errno)
	}
	return fmt.Errorf("%s: Windows API call failed", operation)
}
