//go:build darwin || linux

package advisory

import (
	"errors"
	"syscall"
)

type advisoryNativePublicationLock struct {
	fd uintptr
}

func newAdvisoryNativePublicationLock(fd uintptr) (*advisoryNativePublicationLock, error) {
	return &advisoryNativePublicationLock{fd: fd}, nil
}

func (l *advisoryNativePublicationLock) tryAcquire() (bool, error) {
	err := syscall.Flock(int(l.fd), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func (l *advisoryNativePublicationLock) release() error {
	return syscall.Flock(int(l.fd), syscall.LOCK_UN)
}

func (*advisoryNativePublicationLock) close() error {
	return nil
}
