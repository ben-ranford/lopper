package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
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

var errArtifactParentSymlink = errors.New("artifact parent contains symlink")

var (
	resolveGitBinaryPath      = gitexec.ResolveBinaryPath
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
	explicitFlags  map[string]bool
}

type executionModeRequests struct {
	base     bool
	gitPath  bool
	failure  bool
	publish  bool
	worktree bool
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
	if err := validateExecutionMode(cfg); err != nil {
		return "", err
	}
	switch {
	case publishRequested(cfg):
		return "", publishArtifacts(cfg)
	case strings.TrimSpace(cfg.failureMessage) != "":
		return "", writeFailure(cfg.summaryOut, cfg.statusOut, errors.New(cfg.failureMessage))
	case worktreeRequested(cfg):
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
		if err := writeGitPathArtifact(cfg.gitPathOut, gitPath); err != nil {
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

func validateExecutionMode(cfg config) error {
	if err := validateExplicitOutputPaths(cfg); err != nil {
		return err
	}
	requested := executionModesRequested(cfg)
	if requested.publish && requested.worktree {
		return errors.New("publish mode cannot be combined with worktree mode")
	}
	if requested.worktree && hasWorktreeModeConflict(requested) {
		return errors.New("worktree mode cannot be combined with publish, base-ref, git-path-out, or failure-message")
	}
	if requested.publish && hasPublishModeConflict(requested) {
		return errors.New("publish mode cannot be combined with base-ref, git-path-out, or failure-message")
	}
	if requested.failure && hasFailureModeConflict(requested) {
		return errors.New("failure-message cannot be combined with base-ref or git-path-out")
	}
	if requested.gitPath && !requested.base {
		return errors.New("git-path-out requires base-ref resolution mode")
	}
	return nil
}

func validateExplicitOutputPaths(cfg config) error {
	outputs := []artifactOutput{
		{label: "git-path", path: cfg.gitPathOut},
		{label: "summary", path: cfg.summaryOut},
		{label: "status", path: cfg.statusOut},
		{label: "bench-base", path: cfg.benchBaseOut},
		{label: "bench-head", path: cfg.benchHeadOut},
	}
	for _, output := range outputs {
		flagName := output.label + "-out"
		if !flagWasProvided(cfg, flagName) {
			continue
		}
		if err := validateNonBlankOutputPath(flagName, output.path); err != nil {
			return err
		}
	}
	return nil
}

func executionModesRequested(cfg config) executionModeRequests {
	return executionModeRequests{
		base:     flagWasProvided(cfg, "base-ref"),
		gitPath:  flagWasProvided(cfg, "git-path-out"),
		failure:  flagWasProvided(cfg, "failure-message"),
		publish:  publishRequested(cfg),
		worktree: worktreeRequested(cfg),
	}
}

func hasWorktreeModeConflict(requested executionModeRequests) bool {
	return requested.publish || requested.base || requested.gitPath || requested.failure
}

func hasPublishModeConflict(requested executionModeRequests) bool {
	return requested.base || requested.gitPath || requested.failure
}

func hasFailureModeConflict(requested executionModeRequests) bool {
	return requested.base || requested.gitPath
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
	if len(fs.Args()) != 0 {
		return config{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	cfg.explicitFlags = make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		cfg.explicitFlags[f.Name] = true
	})
	return cfg, nil
}

func publishRequested(cfg config) bool {
	return flagWasProvided(cfg, "summary-input") ||
		flagWasProvided(cfg, "bench-base-input") ||
		flagWasProvided(cfg, "bench-base-out") ||
		flagWasProvided(cfg, "bench-head-input") ||
		flagWasProvided(cfg, "bench-head-out") ||
		flagWasProvided(cfg, "status-code")
}

func worktreeRequested(cfg config) bool {
	return flagWasProvided(cfg, "worktree-add") ||
		flagWasProvided(cfg, "worktree-commit") ||
		flagWasProvided(cfg, "worktree-remove")
}

func flagWasProvided(cfg config, name string) bool {
	return cfg.explicitFlags[name]
}

func resolveBaseCommit(repoRoot, gitPath, baseRef, headRef string) (string, error) {
	if err := validateGitRevisionOperand("base-ref", baseRef); err != nil {
		return "", err
	}
	if err := validateGitRevisionOperand("head-ref", headRef); err != nil {
		return "", err
	}

	if _, err := gitOutput(repoRoot, gitPath, "rev-parse", "--verify", "-q", baseRef+"^{commit}"); err != nil {
		if gitExitCodeIs(err, 1) {
			return "", fmt.Errorf("memory benchmark base ref %q does not resolve to a commit: %w", baseRef, err)
		}
		return "", fmt.Errorf("verify memory benchmark base ref %q: %w", baseRef, err)
	}

	baseCommit, err := gitOutput(repoRoot, gitPath, "merge-base", baseRef, headRef)
	if err != nil {
		if gitExitCodeIs(err, 1) {
			return "", fmt.Errorf("memory benchmark base ref %q is unrelated to %s: %w", baseRef, headRef, err)
		}
		return "", fmt.Errorf("resolve merge-base for %q and %s: %w", baseRef, headRef, err)
	}
	return baseCommit, nil
}

func executeWorktree(repoRoot string, cfg config) error {
	if publishRequested(cfg) || strings.TrimSpace(cfg.failureMessage) != "" || strings.TrimSpace(cfg.baseRef) != "" || strings.TrimSpace(cfg.gitPathOut) != "" {
		return errors.New("worktree mode cannot be combined with publish, base-ref, git-path-out, or failure-message")
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
		return addWorktreeWithCleanConfig(repoRoot, gitPath, cfg.worktreeAdd, cfg.worktreeCommit)
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
	output, err := gitOutputBytes(repoRoot, gitPath, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func gitOutputBytes(repoRoot, gitPath string, args ...string) ([]byte, error) {
	cmd, err := gitCommandContext(context.Background(), gitPath, args...)
	if err != nil {
		return nil, err
	}
	cmd.Dir = repoRoot
	cmd.Env = gitexec.SanitizedEnv()
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return output, nil
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

func gitExitCodeIs(err error, want int) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	return exitErr.ExitCode() == want
}

func writeFailure(summaryOut, statusOut string, err error) error {
	if writeErr := writeFailureArtifacts(summaryOut, statusOut, err.Error()); writeErr != nil {
		return errors.Join(err, fmt.Errorf("write failure artifacts: %w", writeErr))
	}
	return err
}

func writeFailureArtifacts(summaryOut, statusOut, message string) error {
	if err := validateDistinctArtifactOutputs(artifactOutput{label: "summary", path: summaryOut}, artifactOutput{label: "status", path: statusOut}); err != nil {
		return err
	}

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

func writeGitPathArtifact(path, gitPath string) error {
	return writeArtifactFile(path, []byte(gitPath+"\n"))
}

type artifactSpec struct {
	label   string
	path    string
	content []byte
}

type artifactOutput struct {
	label string
	path  string
}

type fileArtifactRequest struct {
	label      string
	inputPath  string
	outputPath string
}

type artifactHandle struct {
	root     safeio.Root
	fileName string
}

type artifactWriter struct {
	roots   map[string]safeio.Root
	targets map[string]artifactHandle
	written map[string]struct{}
}

func publishArtifacts(cfg config) error {
	specs, err := buildArtifactSpecs(cfg)
	if err != nil {
		return err
	}

	writer := &artifactWriter{
		roots:   make(map[string]safeio.Root),
		targets: make(map[string]artifactHandle),
		written: make(map[string]struct{}),
	}
	if err := writer.PrepareAll(specs); err != nil {
		return err
	}
	statusSpec, contentSpecs := splitStatusArtifactSpec(specs)
	if statusSpec != nil {
		if err := writeFailureStatusArtifact(writer, statusSpec.path); err != nil {
			return err
		}
	}
	if err := writeArtifactSpecs(writer, contentSpecs); err != nil {
		return rollbackPublishedArtifacts(writer, statusSpec, err)
	}
	if err := writer.Close(); err != nil {
		return rollbackPublishedArtifacts(writer, statusSpec, err)
	}
	if statusSpec != nil {
		if err := writeArtifactFile(statusSpec.path, statusSpec.content); err != nil {
			return rollbackFinalStatusPublication(writer, statusSpec.path, err)
		}
	}
	return nil
}

func buildArtifactSpecs(cfg config) ([]artifactSpec, error) {
	if err := validatePublishModeConfig(cfg); err != nil {
		return nil, err
	}

	specs := make([]artifactSpec, 0, 4)
	fileRequests := []fileArtifactRequest{
		{label: "bench-base", inputPath: cfg.benchBaseInput, outputPath: cfg.benchBaseOut},
		{label: "bench-head", inputPath: cfg.benchHeadInput, outputPath: cfg.benchHeadOut},
		{label: "summary", inputPath: cfg.summaryInput, outputPath: cfg.summaryOut},
	}
	for _, request := range fileRequests {
		if err := appendFileArtifactSpec(&specs, request); err != nil {
			return nil, err
		}
	}
	if err := appendStatusArtifactSpec(&specs, cfg.statusCode, cfg.statusOut); err != nil {
		return nil, err
	}
	if err := validateArtifactSpecs(specs); err != nil {
		return nil, err
	}
	return specs, nil
}

func validatePublishModeConfig(cfg config) error {
	if strings.TrimSpace(cfg.failureMessage) != "" || strings.TrimSpace(cfg.baseRef) != "" || strings.TrimSpace(cfg.gitPathOut) != "" {
		return errors.New("publish mode cannot be combined with base-ref, git-path-out, or failure-message")
	}
	return nil
}

func appendFileArtifactSpec(specs *[]artifactSpec, request fileArtifactRequest) error {
	switch {
	case request.inputPath == "" && request.outputPath == "":
		return nil
	case request.inputPath == "" || request.outputPath == "":
		return fmt.Errorf("%s publish requires both input and output paths", request.label)
	}
	content, err := readArtifactInput(request.inputPath)
	if err != nil {
		return fmt.Errorf("read %s input: %w", request.label, err)
	}
	*specs = append(*specs, artifactSpec{label: request.label, path: request.outputPath, content: content})
	return nil
}

func appendStatusArtifactSpec(specs *[]artifactSpec, statusCode int, statusOut string) error {
	switch {
	case statusCode < 0 && statusOut == "":
		return nil
	case statusCode < 0 || statusOut == "":
		return errors.New("status publish requires both status-code and status-out")
	case statusCode > 2:
		return fmt.Errorf("status-code must be between 0 and 2: %d", statusCode)
	default:
		*specs = append(*specs, artifactSpec{
			label:   "status",
			path:    statusOut,
			content: []byte(strconv.Itoa(statusCode) + "\n"),
		})
		return nil
	}
}

func validateArtifactSpecs(specs []artifactSpec) error {
	if len(specs) == 0 {
		return errors.New("publish mode requires at least one artifact")
	}
	outputs := make([]artifactOutput, 0, len(specs))
	for _, spec := range specs {
		outputs = append(outputs, artifactOutput{label: spec.label, path: spec.path})
	}
	return validateDistinctArtifactOutputs(outputs...)
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

func splitStatusArtifactSpec(specs []artifactSpec) (*artifactSpec, []artifactSpec) {
	contentSpecs := make([]artifactSpec, 0, len(specs))
	for idx := range specs {
		spec := specs[idx]
		if spec.label == "status" {
			statusSpec := spec
			return &statusSpec, contentSpecs
		}
		contentSpecs = append(contentSpecs, spec)
	}
	return nil, contentSpecs
}

func writeArtifactSpecs(writer *artifactWriter, specs []artifactSpec) error {
	for _, spec := range specs {
		if err := writeArtifactSpec(writer, spec); err != nil {
			return err
		}
	}
	return nil
}

func writeArtifactSpec(writer *artifactWriter, spec artifactSpec) error {
	if err := writer.Write(spec); err != nil {
		return fmt.Errorf("%s artifact: %w", spec.label, err)
	}
	return nil
}

func writeFailureStatusArtifact(writer *artifactWriter, statusPath string) error {
	err := writeArtifactSpec(writer, artifactSpec{
		label:   "status",
		path:    statusPath,
		content: []byte("2\n"),
	})
	if err == nil {
		return nil
	}
	writer.trackWritten(statusPath)
	return rollbackPublishedArtifacts(writer, nil, err)
}

func (w *artifactWriter) PrepareAll(specs []artifactSpec) error {
	for _, spec := range specs {
		if _, err := w.prepare(spec.path); err != nil {
			return errors.Join(fmt.Errorf("%s artifact: %w", spec.label, err), w.Close())
		}
	}
	return nil
}

func (w *artifactWriter) Write(spec artifactSpec) error {
	if w.written == nil {
		w.written = make(map[string]struct{})
	}
	handle, err := w.prepare(spec.path)
	if err != nil {
		return err
	}
	if err := writeArtifactWithinRoot(handle.root, handle.fileName, spec.content, artifactFileMode); err != nil {
		return err
	}
	w.trackWritten(spec.path)
	return handle.root.Chmod(handle.fileName, artifactFileMode)
}

func (w *artifactWriter) trackWritten(path string) {
	if w.written == nil {
		w.written = make(map[string]struct{})
	}
	w.written[path] = struct{}{}
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

func (w *artifactWriter) CleanupWritten() error {
	var errs []error
	for path := range w.written {
		if err := removeArtifactFile(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
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
	currentCreated := false
	for _, part := range relParts {
		nextAbs := filepath.Join(currentAbs, part)
		next, created, err := openOrCreateArtifactFn(current, part, nextAbs)
		if err != nil {
			return nil, "", closeRootWithError(current, err)
		}
		if err := current.Close(); err != nil {
			return nil, "", closeRootWithError(next, err)
		}
		current = next
		currentAbs = nextAbs
		currentCreated = created
	}

	if err := finalizeArtifactDirMode(current, currentAbs, currentCreated); err != nil {
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

func resolveArtifactCollisionKey(path string) (key string, returnErr error) {
	target, err := resolveArtifactTarget(path)
	if err != nil {
		return "", err
	}

	root, existingDir, missingParts, err := openArtifactAncestorFn(target.dir)
	if err != nil {
		if !errors.Is(err, errArtifactParentSymlink) {
			return filepath.Join(target.dir, target.fileName), nil
		}
		return "", err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	caseInsensitive, err := artifactRootCaseInsensitive(root)
	if err != nil {
		return "", err
	}
	key = filepath.Join(appendArtifactPathParts(existingDir, missingParts), target.fileName)
	if caseInsensitive {
		key = strings.ToLower(key)
	}
	return key, nil
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
		next, nextAbs, err := openExistingArtifactParent(current, currentAbs, part)
		switch {
		case os.IsNotExist(err):
			return current, currentAbs, relParts[idx:], nil
		case err != nil:
			return nil, "", nil, closeRootWithError(current, err)
		}
		if err := current.Close(); err != nil {
			return nil, "", nil, closeRootWithError(next, err)
		}
		current = next
		currentAbs = nextAbs
	}

	return current, currentAbs, nil, nil
}

func openExistingArtifactParent(current safeio.Root, currentAbs, part string) (safeio.Root, string, error) {
	nextAbs := filepath.Join(currentAbs, part)
	info, err := current.Lstat(part)
	if err != nil {
		return nil, nextAbs, err
	}
	if err := validateArtifactParentInfo(info, nextAbs); err != nil {
		return nil, nextAbs, err
	}

	next, err := current.OpenRoot(part)
	if err != nil {
		return nil, nextAbs, err
	}
	if err := verifyOpenedArtifactParent(next, info, nextAbs); err != nil {
		return nil, nextAbs, err
	}
	return next, nextAbs, nil
}

func validateArtifactParentInfo(info fs.FileInfo, path string) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", errArtifactParentSymlink, path)
	}
	if !info.IsDir() {
		return fmt.Errorf("artifact parent is not a directory: %s", path)
	}
	return nil
}

func verifyOpenedArtifactParent(root safeio.Root, want fs.FileInfo, path string) error {
	openedInfo, err := root.Lstat(".")
	if err != nil {
		return closeRootWithError(root, err)
	}
	if !os.SameFile(want, openedInfo) {
		return closeRootWithError(root, fmt.Errorf("artifact parent changed while opening: %s", path))
	}
	return nil
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

func openOrCreateArtifactDir(root safeio.Root, name, path string) (safeio.Root, bool, error) {
	created := false
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		if mkdirErr := root.Mkdir(name, artifactDirMode); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
			return nil, false, mkdirErr
		} else if mkdirErr == nil {
			created = true
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("%w: %s", errArtifactParentSymlink, path)
	}
	if !info.IsDir() {
		return nil, false, fmt.Errorf("artifact parent is not a directory: %s", path)
	}

	next, err := root.OpenRoot(name)
	if err != nil {
		return nil, false, err
	}
	openedInfo, err := next.Lstat(".")
	if err != nil {
		return nil, false, closeRootWithError(next, err)
	}
	if !os.SameFile(info, openedInfo) {
		return nil, false, closeRootWithError(next, fmt.Errorf("artifact parent changed while opening: %s", path))
	}
	return next, created, nil
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

func finalizeArtifactDirMode(root safeio.Root, path string, created bool) error {
	if created {
		return root.Chmod(".", artifactDirMode)
	}
	info, err := root.Lstat(".")
	if err != nil {
		return err
	}
	if unsafePermBits(info.Mode().Perm(), artifactDirMode) {
		return fmt.Errorf("artifact root has unsafe permissions: %s", path)
	}
	return nil
}

func unsafePermBits(got, allowed os.FileMode) bool {
	return got&0o022 != 0
}

func validateDistinctArtifactOutputs(outputs ...artifactOutput) error {
	seen := make(map[string]string, len(outputs))
	for _, output := range outputs {
		if output.path == "" {
			continue
		}
		if err := validateNonBlankOutputPath(output.label+"-out", output.path); err != nil {
			return err
		}
		canonicalPath, err := resolveArtifactCollisionKey(output.path)
		if err != nil {
			return fmt.Errorf("%s artifact: %w", output.label, err)
		}
		if prior, ok := seen[canonicalPath]; ok {
			return fmt.Errorf("%s artifact output collides with %s artifact output: %s", output.label, prior, output.path)
		}
		seen[canonicalPath] = output.label
	}
	return nil
}

func validateNonBlankOutputPath(flagName, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s must not be empty or whitespace", flagName)
	}
	return nil
}

func rollbackPublishedArtifacts(writer *artifactWriter, statusSpec *artifactSpec, cause error) error {
	cleanupErr := writer.CleanupWritten()
	closeErr := writer.Close()
	restoreErr := restoreFailureStatus(statusSpec)
	return errors.Join(cause, cleanupErr, closeErr, restoreErr)
}

func rollbackFinalStatusPublication(writer *artifactWriter, statusPath string, cause error) error {
	cleanupErr := writer.CleanupWritten()
	restoreErr := writeStatusArtifact(statusPath)
	return errors.Join(cause, cleanupErr, restoreErr)
}

func restoreFailureStatus(statusSpec *artifactSpec) error {
	if statusSpec == nil {
		return nil
	}
	return writeStatusArtifact(statusSpec.path)
}

func appendArtifactPathParts(base string, parts []string) string {
	for _, part := range parts {
		base = filepath.Join(base, part)
	}
	return base
}

func artifactDirCaseInsensitive(dir string) (caseInsensitive bool, returnErr error) {
	root, _, _, err := openArtifactAncestorFn(dir)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	return artifactRootCaseInsensitive(root)
}

func artifactRootCaseInsensitive(root safeio.Root) (caseInsensitive bool, returnErr error) {
	probeName, probeFile, err := safeio.CreateTempFileWithinRoot(root, "", artifactFileMode)
	if err != nil {
		return false, err
	}
	defer func() {
		if cleanupErr := safeio.CleanupTempFileWithinRoot(root, probeName, probeFile); cleanupErr != nil {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()
	if err := probeFile.Close(); err != nil {
		return false, err
	}

	info, err := root.Lstat(strings.ToUpper(probeName))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	probeInfo, err := root.Lstat(probeName)
	if err != nil {
		return false, err
	}
	return os.SameFile(probeInfo, info), nil
}

func removeArtifactFile(path string) (err error) {
	root, fileName, err := openArtifactDirFn(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	return root.Remove(fileName)
}

func addWorktreeWithCleanConfig(repoRoot, gitPath, worktreePath, commit string) error {
	configArgs, err := cleanWorktreeConfigArgs(repoRoot, gitPath)
	if err != nil {
		return err
	}
	configArgs = append(configArgs, "worktree", "add", "--detach", "--", worktreePath, commit)
	return runGitCommand(repoRoot, gitPath, configArgs...)
}

func cleanWorktreeConfigArgs(repoRoot, gitPath string) ([]string, error) {
	args := append(gitexec.SafeConfigArgs(), "config", "--null", "--includes", "--get-regexp", `^filter\..*\.(smudge|process|clean|required)$`)
	output, err := gitOutputBytes(repoRoot, gitPath, args...)
	if err != nil && !gitExitCodeIs(err, 1) {
		return nil, fmt.Errorf("read effective filter configuration: %w", err)
	}

	configArgs := append([]string{}, gitexec.SafeConfigArgs()...)
	configArgs = append(configArgs, "-c", "core.hooksPath=/dev/null")
	filterNames, err := configuredFilterNames(output)
	if err != nil {
		return nil, err
	}
	for _, filterName := range filterNames {
		configArgs = append(configArgs, "-c", fmt.Sprintf("filter.%s.smudge=", filterName))
		configArgs = append(configArgs, "-c", fmt.Sprintf("filter.%s.process=", filterName))
		configArgs = append(configArgs, "-c", fmt.Sprintf("filter.%s.clean=", filterName))
		configArgs = append(configArgs, "-c", fmt.Sprintf("filter.%s.required=false", filterName))
	}
	return configArgs, nil
}

func configuredFilterNames(configOutput []byte) ([]string, error) {
	if len(configOutput) == 0 {
		return nil, nil
	}
	if configOutput[len(configOutput)-1] != 0 {
		return nil, errors.New("parse effective filter configuration: missing trailing NUL terminator")
	}
	seen := make(map[string]struct{})
	var names []string
	for _, record := range strings.Split(string(configOutput[:len(configOutput)-1]), "\x00") {
		if record == "" {
			continue
		}
		key, _, ok := strings.Cut(record, "\n")
		if !ok {
			return nil, fmt.Errorf("parse effective filter configuration: malformed record %q", record)
		}
		name, ok := filterDriverNameFromConfigKey(strings.TrimSpace(key))
		if !ok {
			return nil, fmt.Errorf("parse effective filter configuration: unexpected key %q", key)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

func filterDriverNameFromConfigKey(key string) (string, bool) {
	const prefix = "filter."
	if !strings.HasPrefix(key, prefix) {
		return "", false
	}
	for _, suffix := range []string{".smudge", ".process", ".clean", ".required"} {
		if strings.HasSuffix(key, suffix) && len(key) > len(prefix)+len(suffix) {
			return key[len(prefix) : len(key)-len(suffix)], true
		}
	}
	return "", false
}
