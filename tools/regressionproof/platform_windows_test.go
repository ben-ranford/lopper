package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func TestWindowsProofMergeBaseOperandBoundary(t *testing.T) {
	testMergeBaseOperandBoundary(t)
}

func TestWindowsProofGitResolver(t *testing.T) {
	regularInfo, directoryInfo := windowsProofResolverFileInfo(t)
	for _, tt := range []struct {
		name        string
		info        os.FileInfo
		statErr     error
		resolved    string
		resolveErr  error
		wantFailure string
	}{
		{name: "regular executable", info: regularInfo, resolved: proofWindowsGitPath},
		{name: "case insensitive path", info: regularInfo, resolved: strings.ToLower(proofWindowsGitPath)},
		{name: "missing executable", statErr: os.ErrNotExist, wantFailure: "locate trusted"},
		{name: "directory", info: directoryInfo, wantFailure: "regular file"},
		{name: "unresolvable executable", info: regularInfo, resolveErr: errors.New("cannot resolve"), wantFailure: "resolve trusted"},
		{name: "redirected executable", info: regularInfo, resolved: `C:\attacker\git.exe`, wantFailure: "must not redirect"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			checkPath := func(path string) {
				t.Helper()
				if path != proofWindowsGitPath {
					t.Fatalf("unexpected executable candidate %q", path)
				}
			}
			actual, err := resolveWindowsProofGitBinaryPath(func(path string) (os.FileInfo, error) {
				checkPath(path)
				return tt.info, tt.statErr
			}, func(path string) (string, error) {
				checkPath(path)
				return tt.resolved, tt.resolveErr
			})
			assertWindowsProofGitResolution(t, actual, err, tt.wantFailure)
		})
	}
}

func assertWindowsProofGitResolution(t *testing.T, actual string, err error, wantFailure string) {
	t.Helper()
	if wantFailure != "" {
		if err == nil || !strings.Contains(err.Error(), wantFailure) || actual != "" {
			t.Fatalf("got path=%q err=%v, want error containing %q", actual, err, wantFailure)
		}
		return
	}
	if err != nil || !strings.EqualFold(actual, proofWindowsGitPath) {
		t.Fatalf("got path=%q err=%v, want trusted path", actual, err)
	}
}

func windowsProofResolverFileInfo(t *testing.T) (os.FileInfo, os.FileInfo) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "git-*.exe")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	regularInfo, err := os.Stat(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return regularInfo, directoryInfo
}

func TestWindowsProofGitEnvironment(t *testing.T) {
	env := windowsProofGitEnv([]string{
		"Path=C:\\attacker", "PATH=C:\\duplicate", "git_dir=C:\\attacker", "Git_Work_Tree=C:\\attacker",
		"git_config_count=1", "GIT_CONFIG_KEY_0=core.fsmonitor", "Git_Config_Value_0=attacker",
		"git_config_global=C:\\attacker", "Ld_PRELOAD=attacker", "dylD_LIBRARY_PATH=attacker",
		"Home=C:\\attacker", "xdg_config_home=C:\\attacker", "XDG_CONFIG_DIRS=C:\\attacker",
		"Pager=attacker", "Editor=attacker", "Visual=attacker", "KEEP_ME=1", "BROKEN",
		"SystemRoot=C:\\Windows", "TEMP=C:\\Temp",
	})
	expected := []string{
		"KEEP_ME=1", "BROKEN", "SystemRoot=C:\\Windows", "TEMP=C:\\Temp",
		proofWindowsSystemPath, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
	configArgs := gitexec.SafeConfigArgs()
	expected = append(expected, "GIT_CONFIG_COUNT="+strconv.Itoa(len(configArgs)/2))
	for index := 0; index < len(configArgs); index += 2 {
		key, value, _ := strings.Cut(configArgs[index+1], "=")
		configIndex := strconv.Itoa(index / 2)
		expected = append(expected, "GIT_CONFIG_KEY_"+configIndex+"="+key, "GIT_CONFIG_VALUE_"+configIndex+"="+value)
	}
	if !reflect.DeepEqual(env, expected) {
		t.Fatalf("unexpected sanitized environment:\n got %#v\nwant %#v", env, expected)
	}
}

func TestWindowsProofNativeTools(t *testing.T) {
	gitPath, err := resolveProofGitBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	if gitPath != proofWindowsGitPath {
		t.Fatalf("unexpected git path %q", gitPath)
	}
	goPath, err := proofGoBinaryPath()
	if err != nil || !filepath.IsAbs(goPath) {
		t.Fatalf("native Go executable was not resolved: path=%q err=%v", goPath, err)
	}
	t.Setenv("gIt_DiR", "attacker")
	for _, entry := range proofGitEnv() {
		if strings.EqualFold(entry, "GIT_DIR=attacker") {
			t.Fatal("inherited Git override survived environment sanitization")
		}
	}
	executor := &execRunner{}
	output, err := executor.Run(context.Background(), gitPath, []string{"--version"}, t.TempDir(), proofGitEnv())
	if err != nil || !strings.HasPrefix(string(output), "git version ") {
		t.Fatalf("trusted Windows git did not execute: output=%q err=%v", output, err)
	}
	output, err = executor.Run(context.Background(), goPath, []string{"version"}, t.TempDir(), os.Environ())
	if err != nil || !strings.HasPrefix(string(output), "go version ") {
		t.Fatalf("native Windows go did not execute: output=%q err=%v", output, err)
	}
}

func TestWindowsProofGoResolver(t *testing.T) {
	root := filepath.Join(proofWindowsGoCache, "1.27.1", "x64")
	path := filepath.Join(root, "bin", "go.exe")
	canonicalRoot := filepath.Join(proofWindowsGoCacheCanonical, "1.27.1", "x64")
	canonicalPath := filepath.Join(canonicalRoot, "bin", "go.exe")
	regularInfo := &stubFileInfo{mode: 0o600}
	for _, tt := range []struct {
		name, root, resolved string
		info                 os.FileInfo
		err                  error
		wantError            bool
	}{
		{name: "hosted toolchain", root: root, resolved: path, info: regularInfo},
		{name: "hosted cache junction", root: root, resolved: canonicalPath, info: regularInfo},
		{name: "canonical hosted cache", root: canonicalRoot, resolved: canonicalPath, info: regularInfo},
		{name: "missing root", wantError: true},
		{name: "relative root", root: "go", wantError: true},
		{name: "foreign root", root: `D:\a\repo\go`, wantError: true},
		{name: "prefix collision", root: proofWindowsGoCache + "-attacker", wantError: true},
		{name: "missing executable", root: root, err: os.ErrNotExist, wantError: true},
		{name: "directory executable", root: root, info: &stubFileInfo{mode: os.ModeDir}, wantError: true},
		{name: "redirected executable", root: root, info: regularInfo, resolved: `D:\a\repo\go.exe`, wantError: true},
		{name: "different version junction", root: root, info: regularInfo, resolved: filepath.Join(proofWindowsGoCacheCanonical, "1.1.1", "x64", "bin", "go.exe"), wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveWindowsProofGoBinaryPath(tt.root, func(string) (os.FileInfo, error) {
				return tt.info, tt.err
			}, func(string) (string, error) { return tt.resolved, nil })
			if (err != nil) != tt.wantError || (err == nil && got != tt.resolved) {
				t.Fatalf("unexpected Go executable: path=%q err=%v", got, err)
			}
		})
	}
}

func TestWindowsProofRejectsUntrustedGoRoot(t *testing.T) {
	t.Setenv("REGRESSION_PROOF_GO_ROOT", `D:\a\repo\go`)
	r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) {
		t.Fatal("invalid toolchain must fail before executing a command")
		return nil, nil
	}}
	if _, err := r.runGo(context.Background(), t.TempDir(), []string{"version"}); err == nil {
		t.Fatal("untrusted Go toolchain was accepted")
	}
}

func TestWindowsProofFinalPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := windowsProofFinalPath(path)
	if err != nil {
		t.Fatalf("resolve opened file: %v", err)
	}
	originalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil || !os.SameFile(originalInfo, resolvedInfo) {
		t.Fatalf("final path must identify the opened file: %q (%v)", resolved, err)
	}
	for _, path := range []string{path + ".missing", "nul\x00path"} {
		if _, err := windowsProofFinalPath(path); err == nil {
			t.Fatalf("invalid path %q was accepted", path)
		}
	}
}

func TestWindowsProofFinalPathRejectsOtherNamespaces(t *testing.T) {
	for _, path := range []string{
		`\\?\C:\Program Files\Git\cmd\git.exe`, `\\?\d:\hostedtoolcache\windows\go\1.27.1\x64\bin\go.exe`,
	} {
		resolved, err := normalizeWindowsProofFinalPath(path)
		if err != nil || resolved != strings.TrimPrefix(path, `\\?\`) {
			t.Fatalf("DOS path %q returned %q, %v", path, resolved, err)
		}
	}
	for _, path := range []string{
		"", `C:\tool.exe`, `\\?\`, `\\?\C:tool.exe`, `\\?\C:/tool.exe`, `\\?\1:\tool.exe`,
		`\\?\UNC\server\share\tool.exe`, `\\?\Volume{guid}\tool.exe`, `\Device\HarddiskVolume1\tool.exe`,
	} {
		if _, err := normalizeWindowsProofFinalPath(path); err == nil {
			t.Fatalf("unexpected path namespace %q was accepted", path)
		}
	}
}
