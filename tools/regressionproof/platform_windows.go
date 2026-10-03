package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"golang.org/x/sys/windows"
)

func newProofGitCommand(ctx context.Context) (*exec.Cmd, error) {
	path, err := resolveGitBinaryPath()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(path, proofWindowsGitPath) {
		return nil, errors.New("unsupported proof Git executable")
	}
	return exec.CommandContext(ctx, proofWindowsGitPath), nil
}

func proofGoSystemPath() string {
	return `C:\mingw64\bin;` + strings.TrimPrefix(proofWindowsSystemPath, "PATH=")
}

func proofCompilerEnv() ([]string, error) {
	// The hosted image's Install-Mingw64.ps1 extracts these compilers to C:\.
	// Keep native cgo enabled as assumed by declaration routing, without
	// inheriting arbitrary CC/CXX commands or caller compiler search paths.
	cc, err := validateWindowsProofExecutable(proofWindowsCCPath, proofWindowsCCPath, "C compiler", os.Stat, windowsProofFinalPath)
	if err != nil {
		return nil, err
	}
	cxx, err := validateWindowsProofExecutable(proofWindowsCXXPath, proofWindowsCXXPath, "C++ compiler", os.Stat, windowsProofFinalPath)
	if err != nil {
		return nil, err
	}
	return []string{"CGO_ENABLED=1", "CC=" + cc, "CXX=" + cxx}, nil
}

const proofWindowsGitPath = `C:\Program Files\Git\cmd\git.exe`
const proofWindowsGoCache = `C:\hostedtoolcache\windows\go`
const proofWindowsGoCacheCanonical = `D:\hostedtoolcache\windows\go`
const proofWindowsCCPath = `C:\mingw64\bin\gcc.exe`
const proofWindowsCXXPath = `C:\mingw64\bin\g++.exe`
const proofWindowsSystemPath = `PATH=C:\Program Files\Git\cmd;C:\Program Files\Git\mingw64\bin;C:\Program Files\Git\usr\bin;C:\Windows\System32;C:\Windows`

func resolveProofGitBinaryPath() (string, error) {
	return resolveWindowsProofGitBinaryPath(os.Stat, windowsProofFinalPath)
}

func resolveWindowsProofGitBinaryPath(stat func(string) (os.FileInfo, error), resolveFinalPath func(string) (string, error)) (string, error) {
	return validateWindowsProofExecutable(proofWindowsGitPath, proofWindowsGitPath, "git", stat, resolveFinalPath)
}

func validateWindowsProofExecutable(path, canonicalPath, name string, stat func(string) (os.FileInfo, error), resolveFinalPath func(string) (string, error)) (string, error) {
	info, err := stat(path)
	if err != nil {
		return "", fmt.Errorf("locate trusted Windows %s executable: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("trusted Windows %s executable must be a regular file: %s", name, path)
	}
	resolved, err := resolveFinalPath(path)
	if err != nil {
		return "", fmt.Errorf("resolve trusted Windows %s executable: %w", name, err)
	}
	resolved = filepath.Clean(resolved)
	if !strings.EqualFold(resolved, path) && !strings.EqualFold(resolved, canonicalPath) {
		return "", fmt.Errorf("trusted Windows %s executable must not redirect to another path: %s", name, resolved)
	}
	return resolved, nil
}

func windowsProofFinalPath(path string) (resolved string, returnErr error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, windows.CloseHandle(handle)) }()
	// Resolve the opened executable directly instead of normalizing each
	// junction ancestor with filepath.EvalSymlinks.
	buffer := make([]uint16, 32768)
	length, err := windows.GetFinalPathNameByHandle(handle, &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return "", err
	}
	if length == 0 || length >= uint32(len(buffer)) {
		return "", errors.New("final Windows executable path is empty or exceeds the supported length")
	}
	return normalizeWindowsProofFinalPath(windows.UTF16ToString(buffer[:length]))
}

func normalizeWindowsProofFinalPath(path string) (string, error) {
	if !strings.HasPrefix(path, `\\?\`) {
		return "", errors.New("final Windows executable path lacks the DOS device prefix")
	}
	dosPath := strings.TrimPrefix(path, `\\?\`)
	if len(dosPath) < 3 || dosPath[1] != ':' || dosPath[2] != '\\' ||
		((dosPath[0] < 'A' || dosPath[0] > 'Z') && (dosPath[0] < 'a' || dosPath[0] > 'z')) {
		return "", errors.New("final Windows executable path must name an absolute DOS drive path")
	}
	return dosPath, nil
}

func proofGitEnv() []string {
	return windowsProofGitEnv(os.Environ())
}

func windowsProofGitEnv(env []string) []string {
	filtered := make([]string, 0, len(env)+4)
	for _, entry := range env {
		key, _, hasKey := strings.Cut(entry, "=")
		if hasKey && stripWindowsProofGitEnvKey(strings.ToUpper(key)) {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, proofWindowsSystemPath, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	args := gitexec.SafeConfigArgs()
	filtered = append(filtered, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(args)/2))
	for index := 0; index < len(args); index += 2 {
		key, value, _ := strings.Cut(args[index+1], "=")
		filtered = append(filtered, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", index/2, key), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", index/2, value))
	}
	return filtered
}

func stripWindowsProofGitEnvKey(key string) bool {
	if strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "LD_") || strings.HasPrefix(key, "DYLD_") {
		return true
	}
	switch key {
	case "PATH", "HOME", "XDG_CONFIG_HOME", "XDG_CONFIG_DIRS", "PAGER", "EDITOR", "VISUAL":
		return true
	default:
		return false
	}
}

func proofGoBinaryPath() (string, error) {
	return resolveWindowsProofGoBinaryPath(os.Getenv("REGRESSION_PROOF_GO_ROOT"), os.Stat, windowsProofFinalPath)
}

func resolveWindowsProofGoBinaryPath(root string, stat func(string) (os.FileInfo, error), resolveFinalPath func(string) (string, error)) (string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || (!strings.HasPrefix(strings.ToLower(root), strings.ToLower(proofWindowsGoCache)+`\`) &&
		!strings.HasPrefix(strings.ToLower(root), strings.ToLower(proofWindowsGoCacheCanonical)+`\`)) {
		return "", fmt.Errorf("windows regression proof requires REGRESSION_PROOF_GO_ROOT under the hosted Go tool cache")
	}
	path := filepath.Join(root, "bin", "go.exe")
	// Hosted setup-go links its C: cache entry to the same version on D:.
	// Permit only that exact correspondence, never an arbitrary junction target.
	canonicalPath := proofWindowsGoCacheCanonical + path[len(proofWindowsGoCache):]
	return validateWindowsProofExecutable(path, canonicalPath, "go", stat, resolveFinalPath)
}
