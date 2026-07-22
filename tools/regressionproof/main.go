package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/prmetadata"
	"github.com/ben-ranford/lopper/internal/safeio"
)

var (
	resolveGitBinaryPath = gitexec.ResolveBinaryPath
	removeAll            = os.RemoveAll
	mkdirTemp            = os.MkdirTemp
	readFileUnder        = safeio.ReadFileUnder
	statFile             = os.Stat
	absPath              = filepath.Abs
)

const buildVCSFlag = "-buildvcs=false"

type testAction string

const (
	testActionPass testAction = "pass"
	testActionFail testAction = "fail"
	testActionSkip testAction = "skip"
)

type goTestEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
}

type confinedWriteRoot interface {
	WriteFileCreatingParents(targetPath string, data []byte, perm, parentPerm os.FileMode) error
	Close() error
}

var openWriteRoot = func(rootDir string) (confinedWriteRoot, error) {
	return safeio.OpenWriteRoot(rootDir)
}

type execRunner struct{}

func (*execRunner) Run(ctx context.Context, name string, args []string, dir string, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := append(stdout.Bytes(), stderr.Bytes()...)
	if err != nil {
		return output, &commandError{name: name, args: args, output: output, err: err}
	}
	return output, nil
}

type commandError struct {
	name   string
	args   []string
	output []byte
	err    error
}

func (e *commandError) Error() string {
	output := strings.TrimSpace(string(e.output))
	if output == "" {
		return fmt.Sprintf("%s %s: %v", e.name, strings.Join(e.args, " "), e.err)
	}
	return fmt.Sprintf("%s %s: %v: %s", e.name, strings.Join(e.args, " "), e.err, output)
}

func (e *commandError) Unwrap() error {
	return e.err
}

type runner struct {
	stderr      io.Writer
	execCommand func(context.Context, string, []string, string, []string) ([]byte, error)
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	execRunner := &execRunner{}
	r := &runner{stderr: stderr, execCommand: execRunner.Run}
	return r.run(args, getenv, stdout)
}

func (r *runner) run(args []string, getenv func(string) string, stdout io.Writer) int {
	fs := flag.NewFlagSet("regressionproof", flag.ContinueOnError)
	fs.SetOutput(r.stderr)
	exemptionLabelDefault := getenv("PR_REGRESSION_EXEMPT_LABEL")
	if exemptionLabelDefault == "" {
		exemptionLabelDefault = "false"
	}
	title := fs.String("title", strings.TrimSpace(getenv("PR_TITLE")), "pull request title")
	repoRoot := fs.String("repo", ".", "repository root")
	baseSHA := fs.String("base-sha", strings.TrimSpace(getenv("PR_BASE_SHA")), "pull request base SHA")
	bodyFile := fs.String("body-file", strings.TrimSpace(getenv("PR_BODY_FILE")), "path to a file containing the pull request body")
	exemptionLabel := fs.String("regression-exempt-label", exemptionLabelDefault, "whether the pull request has the maintainer-controlled regression-exempt label")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	hasExemptionLabel, err := prmetadata.ParseRegressionExemptionLabel(*exemptionLabel)
	if err != nil {
		return writeError(r.stderr, "%v\n", err)
	}

	body, err := readBody(*bodyFile, getenv)
	if err != nil {
		return writeError(r.stderr, "read PR body: %v\n", err)
	}

	metadata, err := prmetadata.ParseRegressionProof(body)
	if err != nil {
		return writeError(r.stderr, "%v\n", err)
	}
	if err := prmetadata.ValidateRegressionRequirements(*title, body, hasExemptionLabel); err != nil {
		return writeError(r.stderr, "%v\n", err)
	}
	if !prmetadata.IsFixTitle(*title) {
		if _, writeErr := fmt.Fprintln(stdout, "Regression proof skipped: non-fix PR."); writeErr != nil {
			return writeError(r.stderr, "write regression proof status: %v\n", writeErr)
		}
		return 0
	}
	if metadata.ExemptionReason != "" {
		if _, writeErr := fmt.Fprintf(stdout, "Regression proof exempted by regression-exempt label: %s\n", metadata.ExemptionReason); writeErr != nil {
			return writeError(r.stderr, "write regression proof status: %v\n", writeErr)
		}
		return 0
	}
	if strings.TrimSpace(*baseSHA) == "" {
		err := errors.New("base SHA is required for regression proof")
		return writeError(r.stderr, "%v\n", err)
	}

	repoAbs, err := absPath(*repoRoot)
	if err != nil {
		return writeError(r.stderr, "resolve repo root: %v\n", err)
	}
	ctx := context.Background()
	if err := r.prove(ctx, repoAbs, strings.TrimSpace(*baseSHA), metadata.Declarations, stdout); err != nil {
		return writeError(r.stderr, "%v\n", err)
	}
	return 0
}

func readBody(bodyFile string, getenv func(string) string) (string, error) {
	if strings.TrimSpace(bodyFile) != "" {
		data, err := safeio.ReadFileLimit(bodyFile, 1<<20)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return getenv("PR_BODY"), nil
}

func (r *runner) prove(ctx context.Context, repoRoot, baseSHA string, declarations []prmetadata.RegressionDeclaration, stdout io.Writer) error {
	mergeBase, err := r.gitOutput(ctx, repoRoot, "merge-base", baseSHA, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve merge base: %w", err)
	}
	mergeBase = strings.TrimSpace(mergeBase)
	if mergeBase == "" {
		return errors.New("resolve merge base: git returned an empty commit")
	}

	changedFiles, err := r.changedFiles(ctx, repoRoot, mergeBase)
	if err != nil {
		return err
	}
	selectedFiles, err := selectProofFiles(changedFiles, declarations)
	if err != nil {
		return err
	}

	worktreeRoot, cleanup, err := r.createBaseWorktree(ctx, repoRoot, mergeBase)
	if err != nil {
		return err
	}
	finish := func(baseErr error) error {
		return errors.Join(baseErr, cleanup())
	}

	if err := copyProofFiles(repoRoot, worktreeRoot, selectedFiles); err != nil {
		return finish(err)
	}

	for _, declaration := range declarations {
		if err := r.compilePackage(ctx, worktreeRoot, declaration.PackagePath); err != nil {
			return finish(fmt.Errorf("base regression test package %s must compile before proof: %w", declaration.PackagePath, err))
		}
		if err := r.expectFailure(ctx, worktreeRoot, declaration); err != nil {
			return finish(err)
		}
		if err := r.expectPass(ctx, repoRoot, declaration); err != nil {
			return finish(err)
		}
		if _, writeErr := fmt.Fprintf(stdout, "Regression proof verified: %s::%s\n", declaration.PackagePath, declaration.TestName); writeErr != nil {
			return finish(writeErr)
		}
	}

	return finish(nil)
}

func (r *runner) changedFiles(ctx context.Context, repoRoot, mergeBase string) ([]string, error) {
	output, err := r.gitOutput(ctx, repoRoot, "diff", "--name-only", "--diff-filter=ACMR", mergeBase+"..HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("list changed files: %w", err)
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		files = append(files, filepath.ToSlash(line))
	}
	return files, nil
}

func selectProofFiles(changedFiles []string, declarations []prmetadata.RegressionDeclaration) ([]string, error) {
	selected := make(map[string]struct{})
	for _, declaration := range declarations {
		packageDir := strings.TrimPrefix(declaration.PackagePath, "./")
		for _, changed := range changedFiles {
			if isProofSupportFile(packageDir, changed) {
				selected[changed] = struct{}{}
			}
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("regression proof requires at least one changed *_test.go file, package testdata fixture, or shared testdata fixture")
	}

	files := make([]string, 0, len(selected))
	for file := range selected {
		files = append(files, file)
	}
	sort.Strings(files)
	return files, nil
}

func isProofSupportFile(packageDir, changed string) bool {
	return isPackageTestFile(packageDir, changed) || isPackageTestdataFile(packageDir, changed) || isSharedTestdataFile(changed)
}

func isPackageTestFile(packageDir, changed string) bool {
	return filepath.ToSlash(filepath.Dir(changed)) == packageDir && strings.HasSuffix(changed, "_test.go")
}

func isPackageTestdataFile(packageDir, changed string) bool {
	prefix := packageDir + "/testdata/"
	return strings.HasPrefix(changed, prefix)
}

func isSharedTestdataFile(changed string) bool {
	return strings.HasPrefix(changed, "testdata/")
}

func (r *runner) createBaseWorktree(ctx context.Context, repoRoot, mergeBase string) (string, func() error, error) {
	worktreeParent, err := mkdirTemp("", "lopper-regression-proof-")
	if err != nil {
		return "", nil, fmt.Errorf("create worktree parent: %w", err)
	}
	worktreePath := filepath.Join(worktreeParent, "base")
	if _, err := r.runGit(ctx, repoRoot, "worktree", "add", "--detach", worktreePath, mergeBase); err != nil {
		if removeErr := removeAll(worktreeParent); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove failed worktree parent: %w", removeErr))
		}
		return "", nil, fmt.Errorf("create base worktree: %w", err)
	}

	cleanup := func() error {
		_, removeWorktreeErr := r.runGit(context.Background(), repoRoot, "worktree", "remove", "--force", worktreePath)
		removeParentErr := removeAll(worktreeParent)
		return errors.Join(removeWorktreeErr, removeParentErr)
	}
	return worktreePath, cleanup, nil
}

func copyProofFiles(headRoot, baseRoot string, files []string) (returnErr error) {
	writeRoot, err := openWriteRoot(baseRoot)
	if err != nil {
		return fmt.Errorf("open base worktree root: %w", err)
	}
	defer func() {
		if closeErr := writeRoot.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close base worktree root: %w", closeErr))
		}
	}()

	for _, rel := range files {
		sourcePath := filepath.Join(headRoot, filepath.FromSlash(rel))
		data, err := readFileUnder(headRoot, sourcePath)
		if err != nil {
			return fmt.Errorf("read changed proof file %s: %w", rel, err)
		}
		info, err := statFile(sourcePath)
		if err != nil {
			return fmt.Errorf("stat changed proof file %s: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("changed proof file %s must be a regular file", rel)
		}
		if err := writeRoot.WriteFileCreatingParents(filepath.FromSlash(rel), data, info.Mode().Perm(), 0o755); err != nil {
			return fmt.Errorf("copy changed proof file %s into base worktree: %w", rel, err)
		}
	}
	return nil
}

func (r *runner) compilePackage(ctx context.Context, repoRoot, packagePath string) (returnErr error) {
	testBinaryDir, err := os.MkdirTemp("", "lopper-regressionproof-test-*")
	if err != nil {
		return fmt.Errorf("create compile output directory: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(testBinaryDir); removeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove compile output directory: %w", removeErr))
		}
	}()

	testBinaryPath := filepath.Join(testBinaryDir, "test-binary")
	_, err = r.execCommand(ctx, "go", []string{"test", buildVCSFlag, "-c", "-o", testBinaryPath, packagePath}, repoRoot, os.Environ())
	return err
}

func (r *runner) expectFailure(ctx context.Context, repoRoot string, declaration prmetadata.RegressionDeclaration) error {
	action, err := r.runDeclaredTest(ctx, repoRoot, declaration)
	if err != nil {
		return fmt.Errorf("run base regression test %s::%s: %w", declaration.PackagePath, declaration.TestName, err)
	}
	switch action {
	case testActionFail:
		return nil
	case testActionSkip:
		return fmt.Errorf("base regression test must fail instead of skip: %s::%s", declaration.PackagePath, declaration.TestName)
	case testActionPass:
		return fmt.Errorf("base regression test unexpectedly passed: %s::%s", declaration.PackagePath, declaration.TestName)
	}
	return fmt.Errorf("base regression test finished without a recognized outcome: %s::%s", declaration.PackagePath, declaration.TestName)
}

func (r *runner) expectPass(ctx context.Context, repoRoot string, declaration prmetadata.RegressionDeclaration) error {
	action, err := r.runDeclaredTest(ctx, repoRoot, declaration)
	if err != nil {
		return fmt.Errorf("pull request regression test must pass on head for %s::%s: %w", declaration.PackagePath, declaration.TestName, err)
	}
	if action == testActionSkip {
		return fmt.Errorf("pull request regression test must pass instead of skip on head for %s::%s", declaration.PackagePath, declaration.TestName)
	}
	if action != testActionPass {
		return fmt.Errorf("pull request regression test must pass on head for %s::%s", declaration.PackagePath, declaration.TestName)
	}
	return nil
}

func (r *runner) runDeclaredTest(ctx context.Context, repoRoot string, declaration prmetadata.RegressionDeclaration) (testAction, error) {
	output, err := r.execCommand(ctx, "go", []string{"test", buildVCSFlag, "-count=1", "-json", "-run", "^" + declaration.TestName + "$", declaration.PackagePath}, repoRoot, os.Environ())
	action, parseErr := parseDeclaredTestAction(output, declaration.TestName)
	if parseErr != nil {
		if err != nil {
			return "", errors.Join(err, parseErr)
		}
		return "", parseErr
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "", err
	}
	return action, nil
}

func parseDeclaredTestAction(output []byte, testName string) (testAction, error) {
	var sawJSON bool
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		sawJSON = true
		var event goTestEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return "", fmt.Errorf("parse go test json for %s: %w", testName, err)
		}
		if event.Test != testName {
			continue
		}
		switch event.Action {
		case string(testActionPass):
			return testActionPass, nil
		case string(testActionFail):
			return testActionFail, nil
		case string(testActionSkip):
			return testActionSkip, nil
		}
	}
	if sawJSON {
		return "", fmt.Errorf("go test did not report an outcome for %s", testName)
	}
	return "", fmt.Errorf("go test did not emit JSON output for %s", testName)
}

func (r *runner) gitOutput(ctx context.Context, repoRoot string, args ...string) (string, error) {
	output, err := r.runGit(ctx, repoRoot, args...)
	return string(output), err
}

func (r *runner) runGit(ctx context.Context, repoRoot string, args ...string) ([]byte, error) {
	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		return nil, err
	}
	fullArgs := append(gitexec.SafeConfigArgs(), "-C", repoRoot)
	fullArgs = append(fullArgs, args...)
	return r.execCommand(ctx, gitPath, fullArgs, repoRoot, gitexec.SanitizedEnv())
}

func writeError(stderr io.Writer, format string, args ...any) int {
	if _, err := fmt.Fprintf(stderr, format, args...); err != nil {
		return 1
	}
	return 1
}
