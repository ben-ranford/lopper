package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/prmetadata"
)

type proofAllocationKind uint8

const (
	proofWorktree proofAllocationKind = iota + 1
	proofCompileOutput
)

type proofAllocation struct {
	root string
	kind proofAllocationKind
}

// Only the allocation sites call this, immediately after creating a private
// temporary directory. Commands cannot nominate an arbitrary output or worktree.
func (r *runner) ownProofAllocation(root, parent, child string, kind proofAllocationKind) error {
	root, err := absPath(root)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(parent) || filepath.Clean(parent) != parent || filepath.Dir(child) != parent {
		return errors.New("proof output must belong to its allocated temporary directory")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("proof allocation must be a real directory")
	}
	if r.allocations == nil {
		r.allocations = make(map[string]proofAllocation)
	}
	r.allocations[child] = proofAllocation{root: root, kind: kind}
	return nil
}

func (r *runner) ownsProofPath(root, path string, kind proofAllocationKind) bool {
	allocation, ok := r.allocations[path]
	return ok && allocation.root == root && allocation.kind == kind
}

func validateProofDeclaration(packagePath, testName string) error {
	declaration := prmetadata.RegressionDeclaration{PackagePath: packagePath, TestName: testName}
	metadata, err := prmetadata.ParseRegressionProof("Regression-Test: " + packagePath + "::" + testName)
	if err != nil || len(metadata.Declarations) != 1 || metadata.Declarations[0] != declaration {
		return errors.New("invalid regression proof package or test operand")
	}
	return nil
}

// The entry points accept only the seven proof operations. They rebuild Go
// requests and validate Git's complete schema; arbitrary executable, flags,
// environment and allocation paths are not part of the production capability.
func (r *runner) runGo(ctx context.Context, repoRoot string, args []string) ([]byte, error) {
	root, err := absPath(repoRoot)
	if err != nil {
		return nil, err
	}
	request, err := r.proofGoRequest(root, args)
	if err != nil {
		return nil, err
	}
	goPath, err := proofGoBinaryPath()
	if err != nil {
		return nil, err
	}
	cmd, err := newProofGoCommand(ctx, goPath)
	if err != nil {
		return nil, err
	}
	cmd.Args = append(cmd.Args, request...)
	cmd.Dir = root
	cmd.Env, err = proofGoEnv(cmd.Path)
	if err != nil {
		return nil, err
	}
	return r.executeProofCommand(ctx, cmd)
}

// Validation belongs inside the executable constructor: its parameter cannot
// select another binary, even when a caller bypasses the normal proof entry point.
func newProofGoCommand(ctx context.Context, path string) (*exec.Cmd, error) {
	trusted, err := proofGoBinaryPath()
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) || path != trusted {
		return nil, errors.New("unsupported proof Go executable")
	}
	return exec.CommandContext(ctx, path), nil
}

func (r *runner) proofGoRequest(root string, args []string) ([]string, error) {
	listPrefix := []string{"list", buildVCSFlag, "-tags", regressionProofBuildTag, "-f", "{{.ImportPath}}"}
	testPrefix := regressionProofGoTestArgs()
	switch {
	case len(args) == 7 && slices.Equal(args[:6], listPrefix):
		if err := validateProofDeclaration(args[6], "TestProofPackage"); err != nil {
			return nil, err
		}
		return append(listPrefix, args[6]), nil
	case len(args) == 8 && slices.Equal(args[:4], testPrefix) && args[4] == "-c" && args[5] == "-o":
		if !r.ownsProofPath(root, args[6], proofCompileOutput) {
			return nil, errors.New("compile output is not owned by this proof")
		}
		if err := validateProofDeclaration(args[7], "TestProofPackage"); err != nil {
			return nil, err
		}
		return regressionProofGoTestArgs("-c", "-o", args[6], args[7]), nil
	case len(args) == 9 && slices.Equal(args[:4], testPrefix) && slices.Equal(args[4:7], []string{"-count=1", "-json", "-run"}):
		pattern := args[7]
		if len(pattern) < 3 || pattern[0] != '^' || pattern[len(pattern)-1] != '$' {
			return nil, errors.New("proof test must use an exact anchored test name")
		}
		if err := validateProofDeclaration(args[8], pattern[1:len(pattern)-1]); err != nil {
			return nil, err
		}
		return regressionProofGoTestArgs("-count=1", "-json", "-run", pattern, args[8]), nil
	default:
		return nil, errors.New("unsupported regression proof Go operation")
	}
}

func (r *runner) runGit(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
	root, err := absPath(repoRoot)
	if err != nil {
		return nil, err
	}
	if err := r.validateProofGitOperation(root, args); err != nil {
		return nil, err
	}
	request := append(gitexec.SafeConfigArgs(), "-c", "core.hooksPath="+os.DevNull, "-C", root)
	request = append(request, args...)
	if err := gitexec.ValidateArgs(request...); err != nil {
		return nil, err
	}
	cmd, err := newProofGitCommand(ctx)
	if err != nil {
		return nil, err
	}
	cmd.Args = append(cmd.Args, request...)
	cmd.Dir = root
	cmd.Env = proofGitEnv()
	return r.executeProofCommand(ctx, cmd)
}

func (r *runner) validateProofGitOperation(root string, args []string) error {
	switch {
	case len(args) == 4 && args[0] == "merge-base" && args[1] == "--" && args[3] == "HEAD":
		return nil // The shared Git schema validates the revision grammar.
	case len(args) == 5 && slices.Equal(args[:3], []string{"diff", "--name-only", "--diff-filter=ACMR"}) && args[4] == "--":
		if strings.HasSuffix(args[3], "..HEAD") && gitexec.ValidObjectID(strings.TrimSuffix(args[3], "..HEAD")) {
			return nil
		}
	case len(args) == 5 && slices.Equal(args[:3], []string{"worktree", "add", "--detach"}):
		if r.ownsProofPath(root, args[3], proofWorktree) && gitexec.ValidObjectID(args[4]) {
			return nil
		}
	case len(args) == 4 && slices.Equal(args[:3], []string{"worktree", "remove", "--force"}):
		if r.ownsProofPath(root, args[3], proofWorktree) {
			return nil
		}
	}
	return errors.New("unsupported or unowned regression proof Git operation")
}

func (r *runner) executeProofCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	if r.execCommand != nil {
		// Test observation is downstream of every production boundary check.
		return r.execCommand(ctx, cmd.Path, cmd.Args[1:], cmd.Dir, cmd.Env)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := stdout.Bytes()
	if err != nil {
		output = append(append([]byte(nil), output...), stderr.Bytes()...)
		return output, &commandError{name: cmd.Path, args: cmd.Args[1:], output: output, err: err}
	}
	return output, nil
}

func proofGoEnv(goPath string) ([]string, error) {
	// No caller-selected Go flags, workspace, compiler, loader, authentication,
	// executable lookup or persisted go env configuration crosses this boundary.
	root := filepath.Dir(filepath.Dir(goPath))
	env := []string{
		"GOROOT=" + root, "GOTOOLCHAIN=local", "GOENV=off", "GOFLAGS=", "GOWORK=off", "GOAUTH=off",
		"GOTELEMETRY=off", "GOOS=" + runtime.GOOS, "GOARCH=" + runtime.GOARCH,
		"PATH=" + filepath.Dir(goPath) + string(os.PathListSeparator) + proofGoSystemPath(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
	for _, key := range []string{"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "SYSTEMROOT", "WINDIR", "TMPDIR", "TMP", "TEMP", "GOCACHE", "GOMODCACHE", "GOPATH"} {
		if value := os.Getenv(key); filepath.IsAbs(value) && !strings.ContainsAny(value, "\x00\r\n") {
			env = append(env, key+"="+value)
		}
	}
	compilerEnv, err := proofCompilerEnv()
	if err != nil {
		return nil, err
	}
	return append(env, compilerEnv...), nil
}

func validateProofGoExecutable(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve proof Go executable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", errors.New("proof Go executable must be a regular executable file")
	}
	// The binary must stay inside the compiler's own root, including installations
	// reached through Homebrew or setup-go symlinks.
	root, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(path)))
	if err != nil || resolved != filepath.Join(root, "bin", "go") {
		return "", errors.New("proof Go executable redirects outside its toolchain")
	}
	return resolved, nil
}
