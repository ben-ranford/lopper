//go:build windows

package runtime

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	runtimeShell32                 = syscall.NewLazyDLL("shell32.dll")
	runtimeOle32                   = syscall.NewLazyDLL("ole32.dll")
	runtimeKernel32                = syscall.NewLazyDLL("kernel32.dll")
	runtimeSHGetKnownFolderPath    = runtimeShell32.NewProc("SHGetKnownFolderPath")
	runtimeCoTaskMemFree           = runtimeOle32.NewProc("CoTaskMemFree")
	runtimeGetSystemDirectory      = runtimeKernel32.NewProc("GetSystemDirectoryW")
	runtimeFolderIDProgramFiles    = syscall.GUID{Data1: 0x905e63b6, Data2: 0xc1bf, Data3: 0x494e, Data4: [8]byte{0xb2, 0x9c, 0x65, 0xb7, 0x32, 0xd3, 0xd2, 0x1a}}
	runtimeFolderIDProgramFilesX86 = syscall.GUID{Data1: 0x7c5a40ef, Data2: 0xa0fb, Data3: 0x4bfc, Data4: [8]byte{0x87, 0x4a, 0xc0, 0xf2, 0xe0, 0xb9, 0xfa, 0x8e}}
	runtimeFolderIDProgramFilesX64 = syscall.GUID{Data1: 0x6d809377, Data2: 0x6af0, Data3: 0x444b, Data4: [8]byte{0x89, 0x57, 0xa3, 0x77, 0x3f, 0x02, 0x20, 0x0e}}
)

func trustedRuntimeSearchDirMode(info os.FileInfo) bool {
	_ = info
	return true
}

func platformRuntimeExecutablePathImmutable(string) bool {
	return false
}

func platformRuntimeWindowsExecutableRoots() []string {
	roots := make([]string, 0, 4)
	for _, folderID := range []*syscall.GUID{
		&runtimeFolderIDProgramFiles,
		&runtimeFolderIDProgramFilesX86,
		&runtimeFolderIDProgramFilesX64,
	} {
		if programFiles := runtimeWindowsKnownFolderPath(folderID); programFiles != "" {
			roots = append(roots, filepath.Join(programFiles, "nodejs"))
		}
	}
	if systemDirectory := runtimeWindowsSystemDirectory(); systemDirectory != "" {
		roots = append(roots, systemDirectory)
	}
	return roots
}

func runtimeWindowsKnownFolderPath(folderID *syscall.GUID) string {
	if runtimeSHGetKnownFolderPath.Find() != nil || runtimeCoTaskMemFree.Find() != nil {
		return ""
	}

	var path *uint16
	result, _, _ := runtimeSHGetKnownFolderPath.Call(
		uintptr(unsafe.Pointer(folderID)),
		0,
		0,
		uintptr(unsafe.Pointer(&path)),
	)
	if result != 0 || path == nil {
		return ""
	}
	defer runtimeCoTaskMemFree.Call(uintptr(unsafe.Pointer(path)))

	return filepath.Clean(runtimeWindowsUTF16PtrToString(path))
}

func runtimeWindowsSystemDirectory() string {
	if runtimeGetSystemDirectory.Find() != nil {
		return ""
	}

	size := uint32(260)
	for range 2 {
		buffer := make([]uint16, size)
		length, _, _ := runtimeGetSystemDirectory.Call(
			uintptr(unsafe.Pointer(&buffer[0])),
			uintptr(len(buffer)),
		)
		if length == 0 {
			return ""
		}
		if length < uintptr(len(buffer)) {
			return filepath.Clean(syscall.UTF16ToString(buffer[:length]))
		}
		size = uint32(length) + 1
	}
	return ""
}

func runtimeWindowsUTF16PtrToString(value *uint16) string {
	length := 0
	for current := unsafe.Pointer(value); *(*uint16)(current) != 0; current = unsafe.Add(current, 2) {
		length++
	}
	return syscall.UTF16ToString(unsafe.Slice(value, length))
}
