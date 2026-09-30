package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

const proofWindowsGitPath = `C:\Program Files\Git\cmd\git.exe`
const proofWindowsGoCache = `C:\hostedtoolcache\windows\go`
const proofWindowsGoCacheCanonical = `D:\hostedtoolcache\windows\go`
const proofWindowsSystemPath = `PATH=C:\Program Files\Git\cmd;C:\Program Files\Git\mingw64\bin;C:\Program Files\Git\usr\bin;C:\Windows\System32;C:\Windows`

func resolveProofGitBinaryPath() (string, error) {
	return resolveWindowsProofGitBinaryPath(os.Stat, filepath.EvalSymlinks)
}

func resolveWindowsProofGitBinaryPath(stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) (string, error) {
	return validateWindowsProofExecutable(proofWindowsGitPath, proofWindowsGitPath, "git", stat, evalSymlinks)
}

func validateWindowsProofExecutable(path, canonicalPath, name string, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) (string, error) {
	info, err := stat(path)
	if err != nil {
		return "", fmt.Errorf("locate trusted Windows %s executable: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("trusted Windows %s executable must be a regular file: %s", name, path)
	}
	resolved, err := evalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve trusted Windows %s executable: %w", name, err)
	}
	resolved = filepath.Clean(resolved)
	if !strings.EqualFold(resolved, path) && !strings.EqualFold(resolved, canonicalPath) {
		return "", fmt.Errorf("trusted Windows %s executable must not redirect to another path: %s", name, resolved)
	}
	return resolved, nil
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
	return resolveWindowsProofGoBinaryPath(os.Getenv("REGRESSION_PROOF_GO_ROOT"), os.Stat, filepath.EvalSymlinks)
}

func resolveWindowsProofGoBinaryPath(root string, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) (string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || (!strings.HasPrefix(strings.ToLower(root), strings.ToLower(proofWindowsGoCache)+`\`) &&
		!strings.HasPrefix(strings.ToLower(root), strings.ToLower(proofWindowsGoCacheCanonical)+`\`)) {
		return "", fmt.Errorf("windows regression proof requires REGRESSION_PROOF_GO_ROOT under the hosted Go tool cache")
	}
	path := filepath.Join(root, "bin", "go.exe")
	// Hosted setup-go links its C: cache entry to the same version on D:.
	// Permit only that exact correspondence, never an arbitrary junction target.
	canonicalPath := proofWindowsGoCacheCanonical + path[len(proofWindowsGoCache):]
	return validateWindowsProofExecutable(path, canonicalPath, "go", stat, evalSymlinks)
}
