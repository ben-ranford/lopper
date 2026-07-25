package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	artifactDirMode  = 0o750
	artifactFileMode = 0o600
)

var (
	resolveGitBinaryPath      = gitexec.ResolveBinaryPath
	writeGitPathOut           = os.WriteFile
	gitCommandContext         = gitexec.CommandContext
	openArtifactDirFn         = openArtifactDir
	openArtifactAncestorFn    = openArtifactAncestorRoot
	openOrCreateArtifactFn    = openOrCreateArtifactDir
	openCanonicalArtifactRoot = safeio.OpenCanonicalRoot
	openArtifactInputFile     = safeio.OpenFile
	resolveArtifactPathAbs    = filepath.Abs
	writeArtifactWithinRoot   = safeio.WriteFileReplacingWithinRoot
)

type config struct {
	baseRef        string
	headRef        string
	gitPathOut     string
	summaryOut     string
	statusOut      string
	failureMessage string
	summaryInput   string
	benchBaseInput string
	benchBaseOut   string
	benchHeadInput string
	benchHeadOut   string
	statusCode     int
	worktreeAdd    string
	worktreeCommit string
	worktreeRemove string
}

func main() {
	os.Exit(runMain(os.Args[1:], ".", os.Stdout, os.Stderr))
}

func runMain(args []string, repoRoot string, stdout, stderr io.Writer) int {
	baseCommit, err := execute(args, repoRoot)
	if err != nil {
		if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
			return 2
		}
		return 2
	}

	if _, err := fmt.Fprintln(stdout, baseCommit); err != nil {
		return 2
	}
	return 0
}

func execute(args []string, repoRoot string) (string, error) {
	cfg, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	if publishRequested(cfg) {
		return "", publishArtifacts(cfg)
	}
	if strings.TrimSpace(cfg.failureMessage) != "" {
		return "", writeFailure(cfg.summaryOut, cfg.statusOut, errors.New(cfg.failureMessage))
	}
	if worktreeRequested(cfg) {
		return "", executeWorktree(repoRoot, cfg)
	}
	if strings.TrimSpace(cfg.baseRef) == "" {
		return "", writeFailure(cfg.summaryOut, cfg.statusOut, errors.New("-base-ref is required"))
	}

	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		return "", writeFailure(cfg.summaryOut, cfg.statusOut, err)
	}
	if cfg.gitPathOut != "" {
		if err := writeGitPathOut(cfg.gitPathOut, []byte(gitPath+"\n"), artifactFileMode); err != nil {
			return "", writeFailure(cfg.summaryOut, cfg.statusOut, fmt.Errorf("write git path output: %w", err))
		}
	}

	baseCommit, err := resolveBaseCommit(repoRoot, gitPath, cfg.baseRef, cfg.headRef)
	if err != nil {
		return "", writeFailure(cfg.summaryOut, cfg.statusOut, err)
	}

	return baseCommit, nil
}

func resolveBaseCommitFromArgs(args []string, repoRoot string) (string, error) {
	return execute(args, repoRoot)
}

func parseArgs(args []string) (config, error) {
	fs := flag.NewFlagSet("benchgate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	cfg := config{}
	fs.StringVar(&cfg.baseRef, "base-ref", "", "git ref to compare against HEAD")
	fs.StringVar(&cfg.headRef, "head-ref", "HEAD", "git ref to compare the base against")
	fs.StringVar(&cfg.gitPathOut, "git-path-out", "", "path to write the resolved git executable")
	fs.StringVar(&cfg.summaryOut, "summary-out", "", "path to write the memory benchmark summary on failure")
	fs.StringVar(&cfg.statusOut, "status-out", "", "path to write the memory benchmark status on failure")
	fs.StringVar(&cfg.failureMessage, "failure-message", "", "failure message to write to artifacts and stderr")
	fs.StringVar(&cfg.summaryInput, "summary-input", "", "path to a staged summary file to publish")
	fs.StringVar(&cfg.benchBaseInput, "bench-base-input", "", "path to a staged base benchmark file to publish")
	fs.StringVar(&cfg.benchBaseOut, "bench-base-out", "", "path to publish the base benchmark artifact")
	fs.StringVar(&cfg.benchHeadInput, "bench-head-input", "", "path to a staged head benchmark file to publish")
	fs.StringVar(&cfg.benchHeadOut, "bench-head-out", "", "path to publish the head benchmark artifact")
	fs.IntVar(&cfg.statusCode, "status-code", -1, "status code to publish to the benchmark status artifact")
	fs.StringVar(&cfg.worktreeAdd, "worktree-add", "", "path to create a detached worktree at")
	fs.StringVar(&cfg.worktreeCommit, "worktree-commit", "", "commit to check out when creating a detached worktree")
	fs.StringVar(&cfg.worktreeRemove, "worktree-remove", "", "path to forcibly remove from git worktrees")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	return cfg, nil
}

func publishRequested(cfg config) bool {
	return cfg.summaryInput != "" || cfg.benchBaseInput != "" || cfg.benchBaseOut != "" || cfg.benchHeadInput != "" || cfg.benchHeadOut != "" || cfg.statusCode >= 0
}

func worktreeRequested(cfg config) bool {
	return cfg.worktreeAdd != "" || cfg.worktreeCommit != "" || cfg.worktreeRemove != ""
}

func resolveBaseCommit(repoRoot, gitPath, baseRef, headRef string) (string, error) {
	if err := validateGitRevisionOperand("base-ref", baseRef); err != nil {
		return "", err
	}
	if err := validateGitRevisionOperand("head-ref", headRef); err != nil {
		return "", err
	}

	if _, err := gitOutput(repoRoot, gitPath, "rev-parse", "--verify", "-q", baseRef+"^{commit}"); err != nil {
		return "", fmt.Errorf("memory benchmark base ref %q does not resolve to a commit", baseRef)
	}

	baseCommit, err := gitOutput(repoRoot, gitPath, "merge-base", baseRef, headRef)
	if err != nil {
		return "", fmt.Errorf("memory benchmark base ref %q is unrelated to %s", baseRef, headRef)
	}
	return baseCommit, nil
}

func executeWorktree(repoRoot string, cfg config) error {
	if publishRequested(cfg) || strings.TrimSpace(cfg.failureMessage) != "" || strings.TrimSpace(cfg.baseRef) != "" {
		return errors.New("worktree mode cannot be combined with publish, base-ref, or failure-message")
	}
	if cfg.worktreeAdd != "" {
		if cfg.worktreeRemove != "" {
			return errors.New("worktree add and remove cannot be combined")
		}
		if strings.TrimSpace(cfg.worktreeCommit) == "" {
			return errors.New("worktree add requires worktree-commit")
		}
	} else if cfg.worktreeCommit != "" {
		return errors.New("worktree-commit requires worktree-add")
	}

	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		return err
	}
	switch {
	case cfg.worktreeAdd != "":
		if err := validateGitRevisionOperand("worktree-commit", cfg.worktreeCommit); err != nil {
			return err
		}
		return runGitCommand(repoRoot, gitPath, "worktree", "add", "--detach", "--", cfg.worktreeAdd, cfg.worktreeCommit)
	case cfg.worktreeRemove != "":
		return runGitCommand(repoRoot, gitPath, "worktree", "remove", "--force", "--", cfg.worktreeRemove)
	default:
		return errors.New("worktree mode requires worktree-add or worktree-remove")
	}
}

func validateGitRevisionOperand(name, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("%s must not start with '-': %q", name, value)
	}
	return nil
}

func gitOutput(repoRoot, gitPath string, args ...string) (string, error) {
	cmd, err := gitCommandContext(context.Background(), gitPath, args...)
	if err != nil {
		return "", err
	}
	cmd.Dir = repoRoot
	cmd.Env = gitexec.SanitizedEnv()
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func runGitCommand(repoRoot, gitPath string, args ...string) error {
	cmd, err := gitCommandContext(context.Background(), gitPath, args...)
	if err != nil {
		return err
	}
	cmd.Dir = repoRoot
	cmd.Env = gitexec.SanitizedEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, message)
	}
	return nil
}

func writeFailure(summaryOut, statusOut string, err error) error {
	if writeErr := writeFailureArtifacts(summaryOut, statusOut, err.Error()); writeErr != nil {
		return errors.Join(err, fmt.Errorf("write failure artifacts: %w", writeErr))
	}
	return err
}

func writeFailureArtifacts(summaryOut, statusOut, message string) error {
	var errs []error
	if summaryOut != "" {
		if err := writeSummaryArtifact(summaryOut, message); err != nil {
			errs = append(errs, fmt.Errorf("summary artifact: %w", err))
		}
	}
	if statusOut != "" {
		if err := writeStatusArtifact(statusOut); err != nil {
			errs = append(errs, fmt.Errorf("status artifact: %w", err))
		}
	}
	return errors.Join(errs...)
}

func writeSummaryArtifact(summaryOut, message string) error {
	summary := invalidComparisonSummary(message)
	return writeArtifactFile(summaryOut, []byte(summary))
}

func writeStatusArtifact(statusOut string) error {
	return writeArtifactFile(statusOut, []byte("2\n"))
}

type artifactSpec struct {
	label   string
	path    string
	content []byte
}

type artifactHandle struct {
	root     safeio.Root
	fileName string
}

type artifactWriter struct {
	roots   map[string]safeio.Root
	targets map[string]artifactHandle
}

func publishArtifacts(cfg config) (returnErr error) {
	specs, err := buildArtifactSpecs(cfg)
	if err != nil {
		return err
	}

	writer := &artifactWriter{
		roots:   make(map[string]safeio.Root),
		targets: make(map[string]artifactHandle),
	}
	defer func() {
		if closeErr := writer.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	if err := writer.PrepareAll(specs); err != nil {
		return err
	}

	var errs []error
	for _, spec := range specs {
		if err := writer.Write(spec); err != nil {
			errs = append(errs, fmt.Errorf("%s artifact: %w", spec.label, err))
		}
	}
	return errors.Join(errs...)
}

func buildArtifactSpecs(cfg config) ([]artifactSpec, error) {
	if strings.TrimSpace(cfg.failureMessage) != "" || strings.TrimSpace(cfg.baseRef) != "" {
		return nil, errors.New("publish mode cannot be combined with base-ref or failure-message")
	}

	var specs []artifactSpec
	appendFileSpec := func(label, inputPath, outputPath string) error {
		switch {
		case inputPath == "" && outputPath == "":
			return nil
		case inputPath == "" || outputPath == "":
			return fmt.Errorf("%s publish requires both input and output paths", label)
		}
		content, err := readArtifactInput(inputPath)
		if err != nil {
			return fmt.Errorf("read %s input: %w", label, err)
		}
		specs = append(specs, artifactSpec{label: label, path: outputPath, content: content})
		return nil
	}

	if err := appendFileSpec("bench-base", cfg.benchBaseInput, cfg.benchBaseOut); err != nil {
		return nil, err
	}
	if err := appendFileSpec("bench-head", cfg.benchHeadInput, cfg.benchHeadOut); err != nil {
		return nil, err
	}
	if err := appendFileSpec("summary", cfg.summaryInput, cfg.summaryOut); err != nil {
		return nil, err
	}

	switch {
	case cfg.statusCode < 0 && cfg.statusOut == "":
	case cfg.statusCode < 0 || cfg.statusOut == "":
		return nil, errors.New("status publish requires both status-code and status-out")
	case cfg.statusCode > 2:
		return nil, fmt.Errorf("status-code must be between 0 and 2: %d", cfg.statusCode)
	default:
		specs = append(specs, artifactSpec{
			label:   "status",
			path:    cfg.statusOut,
			content: []byte(strconv.Itoa(cfg.statusCode) + "\n"),
		})
	}

	if len(specs) == 0 {
		return nil, errors.New("publish mode requires at least one artifact")
	}
	return specs, nil
}

func readArtifactInput(path string) (data []byte, returnErr error) {
	file, err := openArtifactInputFile(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	data, err = io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (w *artifactWriter) PrepareAll(specs []artifactSpec) error {
	for _, spec := range specs {
		if _, err := w.prepare(spec.path); err != nil {
			return fmt.Errorf("%s artifact: %w", spec.label, err)
		}
	}
	return nil
}

func (w *artifactWriter) Write(spec artifactSpec) error {
	handle, err := w.prepare(spec.path)
	if err != nil {
		return err
	}
	if err := writeArtifactWithinRoot(handle.root, handle.fileName, spec.content, artifactFileMode); err != nil {
		return err
	}
	return handle.root.Chmod(handle.fileName, artifactFileMode)
}

func (w *artifactWriter) Close() error {
	var errs []error
	for dir, root := range w.roots {
		if root == nil {
			continue
		}
		if err := root.Close(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", dir, err))
		}
	}
	return errors.Join(errs...)
}

func (w *artifactWriter) prepare(path string) (artifactHandle, error) {
	target, err := resolveArtifactTarget(path)
	if err != nil {
		return artifactHandle{}, err
	}
	key := target.dir + "\x00" + target.fileName
	if handle, ok := w.targets[key]; ok {
		return handle, nil
	}

	root, ok := w.roots[target.dir]
	if !ok {
		var fileName string
		root, fileName, err = openArtifactDirFn(path)
		if err != nil {
			return artifactHandle{}, err
		}
		if fileName != target.fileName {
			return artifactHandle{}, closeRootWithError(root, fmt.Errorf("artifact target changed while opening: %s", path))
		}
		w.roots[target.dir] = root
	}

	if err := validateArtifactTarget(root, target.fileName, path); err != nil {
		return artifactHandle{}, err
	}

	handle := artifactHandle{root: root, fileName: target.fileName}
	w.targets[key] = handle
	return handle, nil
}

func validateArtifactTarget(root safeio.Root, fileName, path string) error {
	info, err := root.Lstat(fileName)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact target is a symlink: %s", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact target is not a regular file: %s", path)
		}
		return nil
	case os.IsNotExist(err):
		return nil
	default:
		return err
	}
}

func writeArtifactFile(path string, content []byte) (err error) {
	root, fileName, err := openArtifactDirFn(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	if err := writeArtifactWithinRoot(root, fileName, content, artifactFileMode); err != nil {
		return err
	}
	return root.Chmod(fileName, artifactFileMode)
}

func openArtifactDir(path string) (_ safeio.Root, fileName string, returnErr error) {
	target, err := resolveArtifactTarget(path)
	if err != nil {
		return nil, "", err
	}

	root, rootAbs, relParts, err := openArtifactAncestorFn(target.dir)
	if err != nil {
		return nil, "", err
	}

	current := root
	currentAbs := rootAbs
	for _, part := range relParts {
		nextAbs := filepath.Join(currentAbs, part)
		next, err := openOrCreateArtifactFn(current, part, nextAbs)
		if err != nil {
			return nil, "", closeRootWithError(current, err)
		}
		if err := current.Close(); err != nil {
			return nil, "", closeRootWithError(next, err)
		}
		current = next
		currentAbs = nextAbs
	}

	if err := current.Chmod(".", artifactDirMode); err != nil {
		return nil, "", closeRootWithError(current, err)
	}

	return current, target.fileName, nil
}

type artifactTarget struct {
	dir      string
	fileName string
}

func resolveArtifactTarget(path string) (artifactTarget, error) {
	targetAbs, err := resolveArtifactPathAbs(path)
	if err != nil {
		return artifactTarget{}, fmt.Errorf("resolve artifact path: %w", err)
	}
	fileName := filepath.Base(targetAbs)
	if fileName == "." || fileName == string(os.PathSeparator) {
		return artifactTarget{}, fmt.Errorf("artifact path must name a file: %s", path)
	}
	return artifactTarget{
		dir:      filepath.Dir(targetAbs),
		fileName: fileName,
	}, nil
}

func openArtifactAncestorRoot(targetDir string) (safeio.Root, string, []string, error) {
	rootAbs := filepath.VolumeName(targetDir) + string(os.PathSeparator)
	root, err := openCanonicalArtifactRoot(rootAbs)
	if err != nil {
		return nil, "", nil, fmt.Errorf("open artifact root: %w", err)
	}

	rel, err := filepath.Rel(rootAbs, targetDir)
	if err != nil {
		return nil, "", nil, closeRootWithError(root, fmt.Errorf("resolve artifact root: %w", err))
	}
	relParts := splitArtifactPath(rel)
	if len(relParts) == 0 {
		return root, rootAbs, nil, nil
	}

	current := root
	currentAbs := rootAbs
	for idx, part := range relParts {
		nextAbs := filepath.Join(currentAbs, part)
		info, err := current.Lstat(part)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, "", nil, closeRootWithError(current, fmt.Errorf("artifact parent contains symlink: %s", nextAbs))
			}
			if !info.IsDir() {
				return nil, "", nil, closeRootWithError(current, fmt.Errorf("artifact parent is not a directory: %s", nextAbs))
			}

			next, err := current.OpenRoot(part)
			if err != nil {
				return nil, "", nil, closeRootWithError(current, err)
			}
			openedInfo, err := next.Lstat(".")
			if err != nil {
				return nil, "", nil, closeRootWithError(current, closeRootWithError(next, err))
			}
			if !os.SameFile(info, openedInfo) {
				return nil, "", nil, closeRootWithError(current, closeRootWithError(next, fmt.Errorf("artifact parent changed while opening: %s", nextAbs)))
			}
			if err := current.Close(); err != nil {
				return nil, "", nil, closeRootWithError(next, err)
			}
			current = next
			currentAbs = nextAbs
		case os.IsNotExist(err):
			return current, currentAbs, relParts[idx:], nil
		default:
			return nil, "", nil, closeRootWithError(current, err)
		}
	}

	return current, currentAbs, nil, nil
}

func splitArtifactPath(rel string) []string {
	if rel == "." || rel == "" {
		return nil
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	filtered := parts[:0]
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		filtered = append(filtered, part)
	}
	return filtered
}

func openOrCreateArtifactDir(root safeio.Root, name, path string) (safeio.Root, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		if mkdirErr := root.Mkdir(name, artifactDirMode); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
			return nil, mkdirErr
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("artifact parent contains symlink: %s", path)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("artifact parent is not a directory: %s", path)
	}

	next, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := next.Lstat(".")
	if err != nil {
		return nil, closeRootWithError(next, err)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, closeRootWithError(next, fmt.Errorf("artifact parent changed while opening: %s", path))
	}
	return next, nil
}

func closeRootWithError(root safeio.Root, err error) error {
	if closeErr := root.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}

func invalidComparisonSummary(message string) string {
	return fmt.Sprintf("## Memory Benchmarks\n\nComparison could not be evaluated.\n\n%s\n", message)
}
