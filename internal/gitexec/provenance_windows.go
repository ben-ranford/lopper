//go:build windows

package gitexec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unsafe"
)

const (
	windowsOwnerSecurityInformation = uintptr(0x00000001)
	windowsDACLSecurityInformation  = uintptr(0x00000004)
	windowsSEFileObject             = uintptr(1)
	windowsAccessAllowedACEType     = byte(0)
	windowsAccessDeniedACEType      = byte(1)
	windowsSystemAuditACEType       = byte(2)
	windowsSystemAlarmACEType       = byte(3)
	windowsInheritOnlyACE           = byte(0x08)
	windowsGitExecutableName        = "git.exe"
)

var (
	windowsAdvapi32                 = syscall.NewLazyDLL("advapi32.dll")
	windowsGetNamedSecurityInfoProc = windowsAdvapi32.NewProc("GetNamedSecurityInfoW")
	windowsGetACEProc               = windowsAdvapi32.NewProc("GetAce")
)

type windowsACL struct {
	revision byte
	padding  byte
	size     uint16
	aceCount uint16
	reserved uint16
}

type windowsACEHeader struct {
	aceType  byte
	aceFlags byte
	aceSize  uint16
}

type windowsAllowedACE struct {
	header   windowsACEHeader
	mask     uint32
	sidStart uint32
}

type windowsExecutableSnapshot struct {
	path    string
	mode    fs.FileMode
	info    fs.FileInfo
	owner   string
	entries []windowsAccessEntry
}

type windowsPathInspector func(string) (windowsExecutableSnapshot, error)

const platformSafeSystemPath = "PATH="

func platformExecutableCandidates() []string {
	candidates := make([]string, 0, 10)
	if path, err := exec.LookPath(windowsGitExecutableName); err == nil {
		candidates = append(candidates, path)
	}
	for _, root := range windowsProgramFilesRoots() {
		candidates = append(candidates,
			filepath.Join(root, "Git", "cmd", windowsGitExecutableName),
			filepath.Join(root, "Git", "bin", windowsGitExecutableName),
		)
	}
	return uniqueWindowsCandidates(candidates)
}

func uniqueWindowsCandidates(candidates []string) []string {
	unique := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" || containsWindowsPath(unique, candidate) {
			continue
		}
		unique = append(unique, candidate)
	}
	return unique
}

func containsWindowsPath(paths []string, candidate string) bool {
	for _, path := range paths {
		if strings.EqualFold(path, candidate) {
			return true
		}
	}
	return false
}

func validatePlatformExecutable(path string) error {
	return validateWindowsExecutablePath(path, windowsProgramFilesRoots(), inspectWindowsPath)
}

func validateWindowsExecutablePath(path string, roots []string, inspect windowsPathInspector) error {
	if !localWindowsAbsolutePath(path) || filepath.Clean(path) != path {
		return fmt.Errorf("git executable path must be an absolute clean local path")
	}
	if !strings.EqualFold(filepath.Ext(path), ".exe") {
		return fmt.Errorf("git executable must use the .exe extension")
	}
	root := containingWindowsRoot(path, roots)
	if root == "" {
		return fmt.Errorf("git executable is outside validated Program Files roots")
	}

	parts := windowsExecutablePathParts(root, path)
	first, err := collectWindowsSnapshots(parts, inspect)
	if err != nil {
		return err
	}
	second, err := collectWindowsSnapshots(parts, inspect)
	if err != nil {
		return err
	}
	if !sameWindowsSnapshots(first, second) {
		return fmt.Errorf("git executable provenance changed during validation")
	}
	return nil
}

func localWindowsAbsolutePath(path string) bool {
	volume := filepath.VolumeName(path)
	return filepath.IsAbs(path) &&
		len(volume) == 2 &&
		volume[1] == ':'
}

func windowsProgramFilesRoots() []string {
	roots := []string{
		os.Getenv("ProgramW6432"),
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
	}
	cleaned := make([]string, 0, len(roots))
	for _, root := range roots {
		if !localWindowsAbsolutePath(root) {
			continue
		}
		root = filepath.Clean(root)
		volumeRoot := filepath.VolumeName(root) + string(os.PathSeparator)
		if strings.EqualFold(root, volumeRoot) ||
			!strings.EqualFold(filepath.Dir(root), volumeRoot) {
			continue
		}
		if !containsWindowsPath(cleaned, root) {
			cleaned = append(cleaned, root)
		}
	}
	return cleaned
}

func containingWindowsRoot(path string, roots []string) string {
	var matched string
	for _, root := range roots {
		rel, err := filepath.Rel(root, path)
		if err != nil || filepath.IsAbs(rel) {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			if len(root) > len(matched) {
				matched = root
			}
		}
	}
	return matched
}

func windowsExecutablePathParts(root, path string) []string {
	volumeRoot := filepath.VolumeName(root) + string(os.PathSeparator)
	parts := []string{volumeRoot, root}
	current := root
	rel, _ := filepath.Rel(root, path)
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		parts = append(parts, current)
	}
	return parts
}

func collectWindowsSnapshots(paths []string, inspect windowsPathInspector) ([]windowsExecutableSnapshot, error) {
	snapshots := make([]windowsExecutableSnapshot, 0, len(paths))
	for index, path := range paths {
		snapshot, err := inspect(path)
		if err != nil {
			return nil, fmt.Errorf("inspect git executable path %s: %w", path, err)
		}
		if err := validateWindowsSnapshot(snapshot, index == len(paths)-1, index > 0); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func inspectWindowsPath(path string) (windowsExecutableSnapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return windowsExecutableSnapshot{}, err
	}
	owner, entries, err := readWindowsSecurity(path)
	if err != nil {
		return windowsExecutableSnapshot{}, err
	}
	return windowsExecutableSnapshot{
		path:    path,
		mode:    info.Mode(),
		info:    info,
		owner:   owner,
		entries: entries,
	}, nil
}

func validateWindowsSnapshot(snapshot windowsExecutableSnapshot, executable, strictACL bool) error {
	if snapshot.mode&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return fmt.Errorf("git executable path contains a reparse point: %s", snapshot.path)
	}
	if err := validateWindowsSnapshotAccess(snapshot, strictACL); err != nil {
		return fmt.Errorf("%s: %w", snapshot.path, err)
	}
	if executable {
		if !snapshot.mode.IsRegular() {
			return fmt.Errorf("git executable is not a regular file: %s", snapshot.path)
		}
		return nil
	}
	if !snapshot.mode.IsDir() {
		return fmt.Errorf("git executable ancestor is not a directory: %s", snapshot.path)
	}
	return nil
}

func validateWindowsSnapshotAccess(snapshot windowsExecutableSnapshot, strict bool) error {
	if strict {
		return validateWindowsAccessPolicy(snapshot.owner, snapshot.entries)
	}
	return validateWindowsStructuralPolicy(snapshot.owner, snapshot.entries)
}

func sameWindowsSnapshots(first, second []windowsExecutableSnapshot) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if !sameWindowsSnapshot(first[index], second[index]) {
			return false
		}
	}
	return true
}

func sameWindowsSnapshot(first, second windowsExecutableSnapshot) bool {
	return first.path == second.path &&
		first.mode == second.mode &&
		os.SameFile(first.info, second.info) &&
		strings.EqualFold(first.owner, second.owner) &&
		slices.Equal(first.entries, second.entries)
}

func readWindowsSecurity(path string) (owner string, entries []windowsAccessEntry, returnErr error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", nil, err
	}

	var ownerSID *syscall.SID
	var dacl *windowsACL
	var descriptor unsafe.Pointer
	result, _, _ := windowsGetNamedSecurityInfoProc.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		windowsSEFileObject,
		windowsOwnerSecurityInformation|windowsDACLSecurityInformation,
		uintptr(unsafe.Pointer(&ownerSID)),
		0,
		uintptr(unsafe.Pointer(&dacl)),
		0,
		uintptr(unsafe.Pointer(&descriptor)),
	)
	if result != 0 {
		return "", nil, syscall.Errno(result)
	}
	defer func() {
		if descriptor == nil {
			return
		}
		_, freeErr := syscall.LocalFree(syscall.Handle(uintptr(descriptor)))
		if freeErr != nil {
			returnErr = errors.Join(returnErr, freeErr)
		}
	}()
	if ownerSID == nil || dacl == nil {
		return "", nil, fmt.Errorf("missing Windows owner or DACL")
	}
	owner, err = ownerSID.String()
	if err != nil {
		return "", nil, err
	}
	entries, err = readWindowsAllowedEntries(dacl)
	if err != nil {
		return "", nil, err
	}
	return owner, entries, nil
}

func readWindowsAllowedEntries(dacl *windowsACL) ([]windowsAccessEntry, error) {
	entries := make([]windowsAccessEntry, 0, dacl.aceCount)
	for index := uint32(0); index < uint32(dacl.aceCount); index++ {
		entry, include, err := readWindowsAllowedEntry(dacl, index)
		if err != nil {
			return nil, err
		}
		if include {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func readWindowsAllowedEntry(dacl *windowsACL, index uint32) (windowsAccessEntry, bool, error) {
	var ace unsafe.Pointer
	ok, _, callErr := windowsGetACEProc.Call(
		uintptr(unsafe.Pointer(dacl)),
		uintptr(index),
		uintptr(unsafe.Pointer(&ace)),
	)
	if ok == 0 {
		return windowsAccessEntry{}, false, callErr
	}
	header := (*windowsACEHeader)(ace)
	switch header.aceType {
	case windowsAccessDeniedACEType, windowsSystemAuditACEType, windowsSystemAlarmACEType:
		return windowsAccessEntry{}, false, nil
	case windowsAccessAllowedACEType:
		return windowsAllowedEntry(ace)
	default:
		return windowsAccessEntry{}, false, fmt.Errorf("unsupported Windows ACE type: %d", header.aceType)
	}
}

func windowsAllowedEntry(ace unsafe.Pointer) (windowsAccessEntry, bool, error) {
	allowed := (*windowsAllowedACE)(ace)
	if allowed.header.aceSize < uint16(unsafe.Sizeof(windowsAllowedACE{})) {
		return windowsAccessEntry{}, false, fmt.Errorf("malformed Windows allow ACE")
	}
	sid := (*syscall.SID)(unsafe.Pointer(&allowed.sidStart))
	principal, err := sid.String()
	if err != nil {
		return windowsAccessEntry{}, false, err
	}
	return windowsAccessEntry{
		principal:   principal,
		mask:        allowed.mask,
		inheritOnly: allowed.header.aceFlags&windowsInheritOnlyACE != 0,
	}, true, nil
}
