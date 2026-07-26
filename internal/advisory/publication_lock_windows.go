//go:build windows

package advisory

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	kernel32CreateMutexW = syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW")
	kernel32ReleaseMutex = syscall.NewLazyDLL("kernel32.dll").NewProc("ReleaseMutex")
)

type advisoryNativePublicationLock struct {
	handle syscall.Handle
	held   bool
}

func newAdvisoryNativePublicationLock(fd uintptr) (*advisoryNativePublicationLock, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(fd), &info); err != nil {
		return nil, err
	}
	name := fmt.Sprintf(`Global\lopper-advisory-cache-%08x-%08x%08x`, info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	result, _, callErr := kernel32CreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if result == 0 {
		return nil, normalizeWindowsLockError(callErr)
	}
	return &advisoryNativePublicationLock{handle: syscall.Handle(result)}, nil
}

func (l *advisoryNativePublicationLock) tryAcquire() (bool, error) {
	runtime.LockOSThread()
	event, err := syscall.WaitForSingleObject(l.handle, 0)
	if err != nil {
		runtime.UnlockOSThread()
		return false, err
	}
	switch event {
	case syscall.WAIT_OBJECT_0, syscall.WAIT_ABANDONED:
		l.held = true
		return true, nil
	case syscall.WAIT_TIMEOUT:
		runtime.UnlockOSThread()
		return false, nil
	default:
		runtime.UnlockOSThread()
		return false, syscall.EINVAL
	}
}

func (l *advisoryNativePublicationLock) release() error {
	if !l.held {
		return nil
	}
	defer runtime.UnlockOSThread()
	l.held = false
	result, _, callErr := kernel32ReleaseMutex.Call(uintptr(l.handle))
	if result == 0 {
		return normalizeWindowsLockError(callErr)
	}
	return nil
}

func (l *advisoryNativePublicationLock) close() error {
	return syscall.CloseHandle(l.handle)
}

func normalizeWindowsLockError(err error) error {
	if errors.Is(err, syscall.Errno(0)) {
		return syscall.EINVAL
	}
	if err == nil {
		return nil
	}
	return err
}
