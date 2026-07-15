package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

var resolveGitBinaryPathFn = gitexec.ResolveBinaryPath
var collectLockfileGitContextFn = collectLockfileGitContext
var execGitCommandContextFn = gitexec.CommandContext

const (
	gitRevParseSubcommand = "rev-parse"
	gitLsFilesSubcommand  = "ls-files"
	gitOthersFlag         = "--others"
	gitExcludeStandardArg = "--exclude-standard"
	gitCachedFlag         = "--cached"
	gitLiteralPathPrefix  = ":(literal)"
	// Keep aggregate argv comfortably bounded. A single path cannot be split,
	// but real candidates come from WalkDir and remain filesystem/Git bounded.
	gitPathspecBatchPaths = 128
	gitPathspecBatchBytes = 16 * 1024
)

type lockfileGitContext struct {
	changedFiles  map[string]struct{}
	hasGitContext bool
}

type gitFilterPathDriver struct {
	path   string
	driver string
}

func collectLockfileGitContext(ctx context.Context, repoPath string, rules []lockfileRule) (lockfileGitContext, error) {
	isWorktree, err := isGitWorktree(ctx, repoPath)
	if err != nil {
		return lockfileGitContext{}, err
	}
	if !isWorktree {
		return lockfileGitContext{}, nil
	}

	candidatePaths, err := collectLockfileManifestChangeCandidatePaths(ctx, repoPath, rules)
	if err != nil {
		return lockfileGitContext{}, err
	}
	return collectLockfileGitContextForPaths(ctx, repoPath, candidatePaths)
}

func collectLockfileGitContextForPaths(ctx context.Context, repoPath string, candidatePaths []string) (lockfileGitContext, error) {
	trackedPaths, untrackedPaths, err := classifyGitCandidatePaths(ctx, repoPath, candidatePaths)
	if err != nil {
		return lockfileGitContext{}, err
	}
	filteredPaths, err := gitActiveFilterPathDrivers(ctx, repoPath, trackedPaths)
	if err != nil {
		return lockfileGitContext{}, err
	}
	if len(filteredPaths) > 0 {
		return lockfileGitContext{}, newLockfileDriftFilterAmbiguityError(filteredPaths)
	}

	changedFiles, err := gitChangedFilesForClassifiedPaths(ctx, repoPath, trackedPaths, untrackedPaths)
	if err != nil {
		return lockfileGitContext{}, err
	}
	return lockfileGitContext{
		changedFiles:  changedFiles,
		hasGitContext: true,
	}, nil
}

func gitChangedFilesForPaths(ctx context.Context, repoPath string, paths []string) (map[string]struct{}, error) {
	trackedPaths, untrackedPaths, err := classifyGitCandidatePaths(ctx, repoPath, paths)
	if err != nil {
		return nil, err
	}
	return gitChangedFilesForClassifiedPaths(ctx, repoPath, trackedPaths, untrackedPaths)
}

func classifyGitCandidatePaths(ctx context.Context, repoPath string, paths []string) ([]string, []string, error) {
	untrackedPaths, err := gitUntrackedFilesForPaths(ctx, repoPath, paths)
	if err != nil {
		return nil, nil, err
	}
	return excludeGitPaths(paths, untrackedPaths), untrackedPaths, nil
}

func excludeGitPaths(paths, excludedPaths []string) []string {
	excluded := make(map[string]struct{}, len(excludedPaths))
	for _, path := range excludedPaths {
		excluded[path] = struct{}{}
	}
	remaining := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, ok := excluded[path]; !ok {
			remaining = append(remaining, path)
		}
	}
	return remaining
}

func gitChangedFilesForClassifiedPaths(ctx context.Context, repoPath string, trackedPaths, untrackedPaths []string) (map[string]struct{}, error) {
	changed := map[string]struct{}{}
	tracked, err := gitTrackedChangesForPaths(ctx, repoPath, trackedPaths)
	if err != nil {
		return nil, err
	}
	for _, path := range tracked {
		changed[path] = struct{}{}
	}

	for _, path := range untrackedPaths {
		changed[path] = struct{}{}
	}

	return changed, nil
}

func isGitWorktree(ctx context.Context, repoPath string) (bool, error) {
	gitPath, err := resolveGitBinaryPathFn()
	if err != nil {
		hasGitMetadata, inspectErr := hasGitMetadataInAncestry(repoPath)
		if inspectErr != nil {
			return false, errors.Join(fmt.Errorf("resolve git binary: %w", err), fmt.Errorf("inspect git metadata: %w", inspectErr))
		}
		if !hasGitMetadata {
			return false, nil
		}
		return false, err
	}
	command, err := gitCommandContextWithBinary(ctx, gitPath, repoPath, gitRevParseSubcommand, "--is-inside-work-tree")
	if err != nil {
		return false, err
	}
	command.Env = append(command.Env, "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 && bytes.Contains(output, []byte("not a git repository")) {
			return false, nil
		}
		return false, fmt.Errorf("run git rev-parse --is-inside-work-tree: %w", err)
	}
	switch strings.TrimSpace(string(output)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("run git rev-parse --is-inside-work-tree: unexpected output %q", strings.TrimSpace(string(output)))
	}
}

func hasGitMetadataInAncestry(path string) (bool, error) {
	searchDir, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	searchDir, err = filepath.EvalSymlinks(searchDir)
	if err != nil {
		return false, err
	}

	for {
		if _, err := os.Lstat(filepath.Join(searchDir, ".git")); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		parent := filepath.Dir(searchDir)
		if parent == searchDir {
			return false, nil
		}
		searchDir = parent
	}
}

func gitTrackedChangesForPaths(ctx context.Context, repoPath string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	hasHead, err := gitHasVerifiedHead(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	if hasHead {
		return gitDiffNameOnlyForPaths(ctx, repoPath, paths, "HEAD")
	}
	// Unborn HEAD: derive tracked changes from staged + working tree diffs.
	staged, err := gitDiffNameOnlyForPaths(ctx, repoPath, paths, gitCachedFlag)
	if err != nil {
		return nil, err
	}
	unstaged, err := gitDiffNameOnlyForPaths(ctx, repoPath, paths)
	if err != nil {
		return nil, err
	}
	return mergeGitPaths(staged, unstaged), nil
}

func gitHasVerifiedHead(ctx context.Context, repoPath string) (bool, error) {
	command, err := gitCommandContext(ctx, repoPath, gitRevParseSubcommand, "--verify", "--quiet", "HEAD")
	if err != nil {
		return false, err
	}
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("run git rev-parse --verify --quiet HEAD: %w", err)
	}
	return true, nil
}

func gitDiffNameOnlyForPaths(ctx context.Context, repoPath string, paths []string, diffArgs ...string) ([]string, error) {
	groups := make([][]string, 0)
	for _, batch := range gitPathspecBatches(paths) {
		changed, err := gitDiffNameOnly(ctx, repoPath, batch, diffArgs...)
		if err != nil {
			return nil, err
		}
		groups = append(groups, changed)
	}
	return mergeSortedGitPaths(groups...), nil
}

func gitDiffNameOnly(ctx context.Context, repoPath string, paths []string, diffArgs ...string) ([]string, error) {
	args := []string{"diff", "--no-ext-diff", "--no-textconv"}
	args = append(args, diffArgs...)
	args = append(args, "--name-only", "--")
	args = append(args, gitLiteralPathspecs(paths)...)
	command, err := gitCommandContext(ctx, repoPath, args...)
	if err != nil {
		return nil, err
	}
	command.Env = gitexec.SanitizedEnv()
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run git %s: %w", strings.Join(args, " "), err)
	}
	return parseGitOutputLines(output), nil
}

func mergeGitPaths(groups ...[]string) []string {
	if len(groups) == 0 {
		return nil
	}
	merged := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, path := range group {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			merged = append(merged, path)
		}
	}
	return merged
}

func mergeSortedGitPaths(groups ...[]string) []string {
	merged := mergeGitPaths(groups...)
	sort.Strings(merged)
	return merged
}

func gitPathspecBatches(paths []string) [][]string {
	if len(paths) == 0 {
		return nil
	}
	batches := make([][]string, 0, (len(paths)+gitPathspecBatchPaths-1)/gitPathspecBatchPaths)
	start := 0
	batchBytes := 0
	for index, path := range paths {
		pathBytes := gitPathspecArgBytes(path)
		if index > start && (index-start >= gitPathspecBatchPaths || batchBytes+pathBytes > gitPathspecBatchBytes) {
			batches = append(batches, paths[start:index])
			start = index
			batchBytes = 0
		}
		batchBytes += pathBytes
	}
	return append(batches, paths[start:])
}

func gitPathspecArgBytes(path string) int {
	return len(gitLiteralPathPrefix) + len(path) + 1
}

func gitLiteralPathspecs(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	literalPaths := make([]string, 0, len(paths))
	for _, path := range paths {
		literalPaths = append(literalPaths, gitLiteralPathPrefix+path)
	}
	return literalPaths
}

func gitUntrackedFiles(ctx context.Context, repoPath string) ([]string, error) {
	command, err := gitCommandContext(ctx, repoPath, gitLsFilesSubcommand, gitOthersFlag, gitExcludeStandardArg)
	if err != nil {
		return nil, err
	}
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run git %s %s %s: %w", gitLsFilesSubcommand, gitOthersFlag, gitExcludeStandardArg, err)
	}
	return parseGitOutputLines(output), nil
}

func gitUntrackedFilesForPaths(ctx context.Context, repoPath string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	groups := make([][]string, 0)
	for _, batch := range gitPathspecBatches(paths) {
		args := []string{gitLsFilesSubcommand, gitOthersFlag, gitExcludeStandardArg, "-z", "--"}
		args = append(args, gitLiteralPathspecs(batch)...)
		command, err := gitCommandContext(ctx, repoPath, args...)
		if err != nil {
			return nil, err
		}
		output, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("run git %s: %w", strings.Join(args, " "), err)
		}
		groups = append(groups, parseNULTerminatedGitOutput(output))
	}
	return mergeSortedGitPaths(groups...), nil
}

func gitCommandContext(ctx context.Context, repoPath string, args ...string) (*exec.Cmd, error) {
	gitPath, err := resolveGitBinaryPathFn()
	if err != nil {
		return nil, err
	}
	return gitCommandContextWithBinary(ctx, gitPath, repoPath, args...)
}

func gitCommandContextWithBinary(ctx context.Context, gitPath, repoPath string, args ...string) (*exec.Cmd, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	commandArgs := append(gitexec.SafeConfigArgs(), "-C", repoPath)
	commandArgs = append(commandArgs, args...)
	command, err := execGitCommandContextFn(ctx, gitPath, commandArgs...)
	if err != nil {
		return nil, err
	}
	command.Env = sanitizedGitEnv()
	return command, nil
}

func parseGitOutputLines(output []byte) []string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func parseNULTerminatedGitOutput(output []byte) []string {
	fields := parseNULTerminatedGitFields(output)
	if len(fields) == 0 {
		return nil
	}

	lines := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		lines = append(lines, field)
	}
	return lines
}

func parseNULTerminatedGitFields(output []byte) []string {
	if len(output) == 0 {
		return nil
	}

	fields := bytes.Split(output, []byte{0})
	if len(fields) > 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}

	lines := make([]string, 0, len(fields))
	for _, field := range fields {
		lines = append(lines, string(field))
	}
	return lines
}

func gitActiveFilterPathDrivers(ctx context.Context, repoPath string, paths []string) ([]gitFilterPathDriver, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	command, err := gitCommandContext(ctx, repoPath, "check-attr", "--stdin", "-z", "filter")
	if err != nil {
		return nil, err
	}
	command.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run git check-attr --stdin -z filter: %w", err)
	}
	assignments, err := parseGitCheckAttrFilterPathDrivers(paths, output)
	if err != nil {
		return nil, fmt.Errorf("parse git check-attr --stdin -z filter output: %w", err)
	}
	return filterConfiguredGitAttributeDrivers(ctx, repoPath, assignments)
}

func filterConfiguredGitAttributeDrivers(ctx context.Context, repoPath string, assignments []gitFilterPathDriver) ([]gitFilterPathDriver, error) {
	if len(assignments) == 0 {
		return nil, nil
	}

	configured, err := gitExecutableFilterDrivers(ctx, repoPath)
	if err != nil {
		return nil, err
	}

	active := make([]gitFilterPathDriver, 0, len(assignments))
	for _, assignment := range assignments {
		if _, ok := configured[assignment.driver]; ok {
			active = append(active, assignment)
		}
	}
	return active, nil
}

// Git renders explicit drivers named set, unset, or unspecified exactly like
// attribute-state values. Requiring executable config for every returned name
// disambiguates those states while allowing inert ordinary declarations.
func gitExecutableFilterDrivers(ctx context.Context, repoPath string) (map[string]struct{}, error) {
	args := []string{"config", "--null", "--includes", "--get-regexp", `^filter\..*\.(clean|process)$`}
	command, err := gitCommandContext(ctx, repoPath, args...)
	if err != nil {
		return nil, err
	}
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return map[string]struct{}{}, nil
		}
		return nil, fmt.Errorf("run git %s: %w", strings.Join(args, " "), err)
	}
	configured, err := parseGitExecutableFilterConfig(output)
	if err != nil {
		return nil, fmt.Errorf("parse git config --null --includes --get-regexp output: %w", err)
	}
	return configured, nil
}

func parseGitExecutableFilterConfig(output []byte) (map[string]struct{}, error) {
	if len(output) == 0 || output[len(output)-1] != 0 {
		return nil, errors.New("truncated output: missing trailing NUL terminator")
	}

	configured := make(map[string]struct{})
	for index, record := range parseNULTerminatedGitFields(output) {
		key, value, ok := strings.Cut(record, "\n")
		if !ok {
			return nil, fmt.Errorf("record %d missing key/value separator", index)
		}
		driver, ok := gitFilterDriverFromExecutableConfigKey(key)
		if !ok {
			return nil, fmt.Errorf("record %d has unexpected filter command key %q", index, key)
		}
		if strings.TrimSpace(value) != "" {
			configured[driver] = struct{}{}
		}
	}
	return configured, nil
}

func gitFilterDriverFromExecutableConfigKey(key string) (string, bool) {
	const prefix = "filter."
	if len(key) <= len(prefix) || !strings.EqualFold(key[:len(prefix)], prefix) {
		return "", false
	}
	for _, suffix := range []string{".clean", ".process"} {
		if len(key) <= len(prefix)+len(suffix) || !strings.EqualFold(key[len(key)-len(suffix):], suffix) {
			continue
		}
		return key[len(prefix) : len(key)-len(suffix)], true
	}
	return "", false
}

func newLockfileDriftFilterAmbiguityError(assignments []gitFilterPathDriver) error {
	parts := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		parts = append(parts, fmt.Sprintf("%s (%s)", assignment.path, assignment.driver))
	}
	return fmt.Errorf("cannot safely evaluate lockfile drift: active custom git filter drivers on %s", strings.Join(parts, ", "))
}

func parseGitCheckAttrFilterPathDrivers(paths []string, output []byte) ([]gitFilterPathDriver, error) {
	if len(output) == 0 || output[len(output)-1] != 0 {
		return nil, errors.New("truncated output: missing trailing NUL terminator")
	}

	fields := parseNULTerminatedGitFields(output)
	expectedFields := len(paths) * 3
	if len(fields) != expectedFields {
		return nil, fmt.Errorf("expected %d NUL-delimited fields for %d paths, got %d", expectedFields, len(paths), len(fields))
	}

	assignments := make([]gitFilterPathDriver, 0, len(fields)/3)
	for index, expectedPath := range paths {
		fieldIndex := index * 3
		path := fields[fieldIndex]
		if path != expectedPath {
			return nil, fmt.Errorf("path %d mismatch: expected %q, got %q", index, expectedPath, path)
		}
		attribute := fields[fieldIndex+1]
		if attribute != "filter" {
			return nil, fmt.Errorf("attribute %d mismatch for %q: expected %q, got %q", index, expectedPath, "filter", attribute)
		}

		value := strings.TrimSpace(fields[fieldIndex+2])
		if value == "" {
			continue
		}
		assignments = append(assignments, gitFilterPathDriver{
			path:   path,
			driver: value,
		})
	}
	return assignments, nil
}

func sanitizedGitEnv() []string {
	return gitexec.SanitizedEnv()
}
