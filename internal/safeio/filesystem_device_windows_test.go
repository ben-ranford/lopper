//go:build windows

package safeio

import (
	"errors"
	"testing"
)

type windowsHandleTestFile struct {
	fakeFile
	fd uintptr
}

func (f *windowsHandleTestFile) Fd() uintptr {
	return f.fd
}

func TestSameDeviceRootPairUsesPinnedRootHandlesOnWindows(t *testing.T) {
	previous := windowsHandleFileIdentityFn
	defer func() {
		windowsHandleFileIdentityFn = previous
	}()

	var gotFDs []uintptr
	windowsHandleFileIdentityFn = func(fd uintptr) (windowsHandleFileIdentity, error) {
		gotFDs = append(gotFDs, fd)
		switch fd {
		case 11:
			return windowsHandleFileIdentity{volumeSerial: 7, fileIndexHi: 1, fileIndexLo: 2}, nil
		case 22:
			return windowsHandleFileIdentity{volumeSerial: 7, fileIndexHi: 3, fileIndexLo: 4}, nil
		default:
			return windowsHandleFileIdentity{}, errors.New("unexpected handle")
		}
	}

	parent := &fakeRoot{
		open: func(string) (File, error) {
			return &windowsHandleTestFile{fd: 11}, nil
		},
	}
	child := &fakeRoot{
		open: func(string) (File, error) {
			return &windowsHandleTestFile{fd: 22}, nil
		},
	}

	if !sameDeviceRootPair(parent, child, nil, nil) {
		t.Fatal("expected same-device roots to be accepted when pinned handles share a volume")
	}
	if len(gotFDs) != 2 || gotFDs[0] != 11 || gotFDs[1] != 22 {
		t.Fatalf("expected pinned root handles to be inspected, got %v", gotFDs)
	}
}

func TestSameDeviceRootPairFailsClosedOnWindowsIdentityErrors(t *testing.T) {
	previous := windowsHandleFileIdentityFn
	defer func() {
		windowsHandleFileIdentityFn = previous
	}()

	windowsHandleFileIdentityFn = func(uintptr) (windowsHandleFileIdentity, error) {
		return windowsHandleFileIdentity{}, errors.New("identity unavailable")
	}

	withHandle := &fakeRoot{
		open: func(string) (File, error) {
			return &windowsHandleTestFile{fd: 11}, nil
		},
	}
	if sameDeviceRootPair(withHandle, withHandle, nil, nil) {
		t.Fatal("expected identity lookup failure to reject traversal")
	}

	withoutHandle := &fakeRoot{
		open: func(string) (File, error) {
			return &fakeFile{}, nil
		},
	}
	if sameDeviceRootPair(withoutHandle, withHandle, nil, nil) {
		t.Fatal("expected missing file-descriptor support to reject traversal")
	}
}

func TestSameDeviceRootPairRejectsDifferentWindowsVolumes(t *testing.T) {
	previous := windowsHandleFileIdentityFn
	defer func() {
		windowsHandleFileIdentityFn = previous
	}()

	windowsHandleFileIdentityFn = func(fd uintptr) (windowsHandleFileIdentity, error) {
		if fd == 11 {
			return windowsHandleFileIdentity{volumeSerial: 7, fileIndexHi: 1, fileIndexLo: 2}, nil
		}
		return windowsHandleFileIdentity{volumeSerial: 8, fileIndexHi: 1, fileIndexLo: 2}, nil
	}

	parent := &fakeRoot{open: func(string) (File, error) { return &windowsHandleTestFile{fd: 11}, nil }}
	child := &fakeRoot{open: func(string) (File, error) { return &windowsHandleTestFile{fd: 22}, nil }}

	if sameDeviceRootPair(parent, child, nil, nil) {
		t.Fatal("expected different Windows volumes to be rejected")
	}
}
