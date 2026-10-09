package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/prmetadata"
	"github.com/ben-ranford/lopper/internal/safeio"
)

var (
	resolveGitBinaryPath = resolveProofGitBinaryPath
	removeAll            = os.RemoveAll
	mkdirTemp            = os.MkdirTemp
	readFileUnder        = safeio.ReadFileUnder
	statFile             = os.Stat
	absPath              = filepath.Abs
)

const (
	buildVCSFlag            = "-buildvcs=false"
	regressionProofBuildTag = "regressionproof"
	proofStatusWriteError   = "write regression proof status: %v\n"
)

type testAction string

type declaredTestResult struct {
	action testAction
	output []byte
}

const (
	testActionPass testAction = "pass"
	testActionFail testAction = "fail"
	testActionSkip testAction = "skip"
)

type goTestEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
}

type confinedWriteRoot interface {
	WriteFileCreatingParents(targetPath string, data []byte, perm, parentPerm os.FileMode) error
	Close() error
}

var openWriteRoot = func(rootDir string) (confinedWriteRoot, error) {
	return safeio.OpenWriteRoot(rootDir)
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
	allocations map[string]proofAllocation
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	r := &runner{stderr: stderr}
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
	targetOS := fs.String("target-os", "", "select declarations for the required linux, windows or darwin proof job")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := validateProofTarget(*targetOS); err != nil {
		return writeError(r.stderr, "%v\n", err)
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
			return writeError(r.stderr, proofStatusWriteError, writeErr)
		}
		return 0
	}
	if metadata.ExemptionReason != "" {
		if _, writeErr := fmt.Fprintf(stdout, "Regression proof exempted by regression-exempt label: %s\n", metadata.ExemptionReason); writeErr != nil {
			return writeError(r.stderr, proofStatusWriteError, writeErr)
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
	declarations, err := selectTargetDeclarations(repoAbs, metadata.Declarations, *targetOS)
	if err != nil {
		return writeError(r.stderr, "%v\n", err)
	}
	if len(declarations) == 0 {
		if _, err := fmt.Fprintf(stdout, "No regression declarations assigned to %s; the other required native proof job must verify them.\n", *targetOS); err != nil {
			return writeError(r.stderr, proofStatusWriteError, err)
		}
		return 0
	}
	if err := requireNativeProofTarget(*targetOS); err != nil {
		return writeError(r.stderr, "%v\n", err)
	}
	ctx := context.Background()
	if err := r.prove(ctx, repoAbs, strings.TrimSpace(*baseSHA), declarations, stdout); err != nil {
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
	mergeBase, err := r.gitOutput(ctx, repoRoot, "merge-base", "--", baseSHA, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve merge base: %w", err)
	}
	mergeBase = strings.TrimSpace(mergeBase)
	if !gitexec.ValidObjectID(mergeBase) {
		return errors.New("resolve merge base: git must return one full commit OID")
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
		basePackage, err := r.resolvePackage(ctx, worktreeRoot, declaration.PackagePath)
		if err != nil {
			return finish(err)
		}
		if err := r.compilePackage(ctx, worktreeRoot, declaration.PackagePath); err != nil {
			return finish(fmt.Errorf("base regression test package %s must compile before proof: %w", declaration.PackagePath, err))
		}
		if err := r.expectFailure(ctx, worktreeRoot, basePackage, declaration, stdout); err != nil {
			return finish(err)
		}
		headPackage, err := r.resolvePackage(ctx, repoRoot, declaration.PackagePath)
		if err != nil {
			return finish(err)
		}
		if headPackage != basePackage {
			return finish(fmt.Errorf("regression-test package identity changed between base and head: %s != %s", basePackage, headPackage))
		}
		if err := r.expectPass(ctx, repoRoot, headPackage, declaration); err != nil {
			return finish(err)
		}
		if _, writeErr := fmt.Fprintf(stdout, "Regression proof verified: %s::%s runner=%s/%s base=fail head=pass base_commit=%s\n", declaration.PackagePath, declaration.TestName, runtime.GOOS, runtime.GOARCH, mergeBase); writeErr != nil {
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
	return filepath.ToSlash(filepath.Dir(changed)) == packageDir &&
		strings.HasSuffix(changed, "_test.go") &&
		!strings.HasSuffix(changed, "_head_test.go")
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
	if err := r.ownProofAllocation(repoRoot, worktreeParent, worktreePath, proofWorktree); err != nil {
		return "", nil, errors.Join(err, removeAll(worktreeParent))
	}
	if _, err := r.runGit(ctx, repoRoot, "worktree", "add", "--detach", worktreePath, mergeBase); err != nil {
		delete(r.allocations, worktreePath)
		if removeErr := removeAll(worktreeParent); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove failed worktree parent: %w", removeErr))
		}
		return "", nil, fmt.Errorf("create base worktree: %w", err)
	}

	cleanup := func() error {
		_, removeWorktreeErr := r.runGit(context.Background(), repoRoot, "worktree", "remove", "--force", worktreePath)
		delete(r.allocations, worktreePath)
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

func (r *runner) resolvePackage(ctx context.Context, repoRoot, packagePath string) (string, error) {
	args := []string{"list", buildVCSFlag, "-tags", regressionProofBuildTag, "-f", "{{.ImportPath}}", packagePath}
	output, err := r.runGo(ctx, repoRoot, args)
	if err != nil {
		return "", fmt.Errorf("resolve regression-test package %s: %w", packagePath, err)
	}

	var packages []string
	for _, line := range strings.Split(string(output), "\n") {
		if importPath := strings.TrimSpace(line); importPath != "" {
			packages = append(packages, importPath)
		}
	}
	if len(packages) != 1 {
		return "", fmt.Errorf("regression-test package path %s must resolve to exactly one package; got %d", packagePath, len(packages))
	}
	return packages[0], nil
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
	if err := r.ownProofAllocation(repoRoot, testBinaryDir, testBinaryPath, proofCompileOutput); err != nil {
		return err
	}
	defer delete(r.allocations, testBinaryPath)
	_, err = r.runGo(ctx, repoRoot, regressionProofGoTestArgs("-c", "-o", testBinaryPath, packagePath))
	return err
}

func (r *runner) expectFailure(ctx context.Context, repoRoot, expectedPackage string, declaration prmetadata.RegressionDeclaration, stdout io.Writer) error {
	result, err := r.runDeclaredTest(ctx, repoRoot, expectedPackage, declaration)
	if err != nil {
		return fmt.Errorf("run base regression test %s::%s: %w", declaration.PackagePath, declaration.TestName, err)
	}
	switch result.action {
	case testActionFail:
		return writeBaseFailureOutput(stdout, declaration, result.output)
	case testActionSkip:
		return fmt.Errorf("base regression test must fail instead of skip: %s::%s", declaration.PackagePath, declaration.TestName)
	case testActionPass:
		return fmt.Errorf("base regression test unexpectedly passed: %s::%s", declaration.PackagePath, declaration.TestName)
	}
	return fmt.Errorf("base regression test finished without a recognized outcome: %s::%s", declaration.PackagePath, declaration.TestName)
}

func writeBaseFailureOutput(stdout io.Writer, declaration prmetadata.RegressionDeclaration, output []byte) error {
	if _, err := fmt.Fprintf(stdout, "Expected base failure: %s::%s\n", declaration.PackagePath, declaration.TestName); err != nil {
		return fmt.Errorf("write base regression failure status: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		// Escape controls and hashes so modern and legacy workflow commands stay literal.
		quoted := strings.ReplaceAll(strconv.Quote(line), "#", `\x23`)
		if _, err := fmt.Fprintf(stdout, "base-test-output: %s\n", quoted); err != nil {
			return fmt.Errorf("write base regression failure output: %w", err)
		}
	}
	return nil
}

func (r *runner) expectPass(ctx context.Context, repoRoot, expectedPackage string, declaration prmetadata.RegressionDeclaration) error {
	result, err := r.runDeclaredTest(ctx, repoRoot, expectedPackage, declaration)
	if err != nil {
		return fmt.Errorf("pull request regression test must pass on head for %s::%s: %w", declaration.PackagePath, declaration.TestName, err)
	}
	if result.action == testActionSkip {
		return fmt.Errorf("pull request regression test must pass instead of skip on head for %s::%s", declaration.PackagePath, declaration.TestName)
	}
	if result.action != testActionPass {
		return fmt.Errorf("pull request regression test must pass on head for %s::%s", declaration.PackagePath, declaration.TestName)
	}
	return nil
}

func (r *runner) runDeclaredTest(ctx context.Context, repoRoot, expectedPackage string, declaration prmetadata.RegressionDeclaration) (declaredTestResult, error) {
	output, err := r.runGo(ctx, repoRoot, regressionProofGoTestArgs("-count=1", "-json", "-run", "^"+declaration.TestName+"$", declaration.PackagePath))
	action, parseErr := parseDeclaredTestAction(output, expectedPackage, declaration.TestName)
	if parseErr != nil {
		if err != nil {
			return declaredTestResult{}, errors.Join(err, parseErr)
		}
		return declaredTestResult{}, parseErr
	}
	var exitErr *exec.ExitError
	if err != nil && (action == testActionPass || !errors.As(err, &exitErr)) {
		return declaredTestResult{}, err
	}
	return declaredTestResult{action: action, output: output}, nil
}

func regressionProofGoTestArgs(args ...string) []string {
	result := []string{"test", buildVCSFlag, "-tags", regressionProofBuildTag}
	return append(result, args...)
}

func parseDeclaredTestAction(output []byte, expectedPackage, testName string) (testAction, error) {
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
		if event.Package != expectedPackage || event.Test != testName {
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

func writeError(stderr io.Writer, format string, args ...any) int {
	if _, err := fmt.Fprintf(stderr, format, args...); err != nil {
		return 1
	}
	return 1
}
