//go:build !windows

package main

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func resolveProofGitBinaryPath() (string, error) {
	return gitexec.ResolveBinaryPath()
}

func proofGitEnv() []string {
	return gitexec.SanitizedEnv()
}

func proofGoBinaryPath() (string, error) {
	// runtime.GOROOT() accepts a process-start GOROOT override. The compiled
	// source location of runtime.Version instead pins the compiler that built
	// this proof binary, including a toolchain downloaded by the Go launcher.
	pc := reflect.ValueOf(runtime.Version).Pointer()
	source, _ := runtime.FuncForPC(pc).FileLine(pc)
	if !filepath.IsAbs(source) || filepath.Base(filepath.Dir(source)) != "runtime" || filepath.Base(filepath.Dir(filepath.Dir(source))) != "src" {
		return "", errors.New("proof requires an absolute compiled Go runtime source location; rebuild without trimpath")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(source)))
	return validateProofGoExecutable(filepath.Join(root, "bin", "go"))
}

func newProofGitCommand(ctx context.Context) (*exec.Cmd, error) {
	path, err := resolveGitBinaryPath()
	if err != nil {
		return nil, err
	}
	switch path {
	case gitexec.ExecutablePrimary:
		return exec.CommandContext(ctx, "/usr/bin/git"), nil
	case gitexec.ExecutableFallback:
		return exec.CommandContext(ctx, "/bin/git"), nil
	default:
		return nil, errors.New("unsupported proof Git executable")
	}
}

func proofGoSystemPath() string {
	return "/usr/bin:/bin:/usr/sbin:/sbin"
}

func proofCompilerEnv() ([]string, error) {
	if runtime.GOOS == "darwin" {
		return []string{"CGO_ENABLED=1", "CC=/usr/bin/clang", "CXX=/usr/bin/clang++"}, nil
	}
	return []string{"CGO_ENABLED=1", "CC=/usr/bin/gcc", "CXX=/usr/bin/g++"}, nil
}
