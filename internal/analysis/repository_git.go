package analysis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

const repositoryGitFilterProbeMarker = "__LOPPER_REPOSITORY_FILTER_PROBE__"

var resolveRepositoryGitBinaryPathFn = gitexec.ResolveBinaryPath
var repositoryGitWorktreeFn = repositoryIsGitWorktree
var repositoryExecutableFiltersFn = repositoryExecutableFilterDrivers
var repositoryActiveFiltersFn = repositoryActiveFilterDrivers
var repositoryCurrentCommitFn = repositoryCurrentCommit
var repositoryChangedFilesFn = repositoryChangedFiles

// RepositoryGitFilter identifies a tracked path whose configured clean or
// process filter makes raw lockfile comparisons unsafe.
type RepositoryGitFilter struct {
	Path   string
	Driver string
}

// RepositoryGitMetadata is the Git-sensitive state sealed into a
// RepositoryView. Captured distinguishes a clean/non-Git repository from a
// view opened without metadata capture.
type RepositoryGitMetadata struct {
	Captured            bool
	IsWorktree          bool
	CurrentCommit       string
	ChangedFiles        []string
	ActiveFilterDrivers []RepositoryGitFilter
	CaptureError        error
}

func (m *RepositoryGitMetadata) clone() RepositoryGitMetadata {
	if m == nil {
		return RepositoryGitMetadata{}
	}
	cloned := *m
	cloned.ChangedFiles = append([]string{}, m.ChangedFiles...)
	cloned.ActiveFilterDrivers = append([]RepositoryGitFilter{}, m.ActiveFilterDrivers...)
	return cloned
}

func (m *RepositoryGitMetadata) equal(other RepositoryGitMetadata) bool {
	return m.Captured == other.Captured &&
		m.IsWorktree == other.IsWorktree &&
		m.CurrentCommit == other.CurrentCommit &&
		slices.Equal(m.ChangedFiles, other.ChangedFiles) &&
		slices.Equal(m.ActiveFilterDrivers, other.ActiveFilterDrivers) &&
		errorText(m.CaptureError) == errorText(other.CaptureError)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func captureRepositoryGitMetadata(ctx context.Context, repoPath string) RepositoryGitMetadata {
	metadata := RepositoryGitMetadata{Captured: true}
	gitPath, err := resolveRepositoryGitBinaryPathFn()
	if err != nil {
		metadata.CaptureError = fmt.Errorf("resolve git binary: %w", err)
		return metadata
	}

	metadata.IsWorktree, err = repositoryGitWorktreeFn(ctx, gitPath, repoPath)
	if err != nil || !metadata.IsWorktree {
		metadata.CaptureError = err
		return metadata
	}

	configuredDrivers, err := repositoryExecutableFiltersFn(ctx, gitPath, repoPath)
	if err != nil {
		metadata.CaptureError = err
		return metadata
	}
	metadata.ActiveFilterDrivers, err = repositoryActiveFiltersFn(ctx, gitPath, repoPath, configuredDrivers)
	if err != nil {
		metadata.CaptureError = err
		return metadata
	}

	var hasHead bool
	metadata.CurrentCommit, hasHead, err = repositoryCurrentCommitFn(ctx, gitPath, repoPath)
	if err != nil {
		metadata.CaptureError = err
		return metadata
	}
	metadata.ChangedFiles, err = repositoryChangedFilesFn(ctx, gitPath, repoPath, configuredDrivers, hasHead)
	if err != nil {
		metadata.CaptureError = err
	}
	return metadata
}

func repositoryIsGitWorktree(ctx context.Context, gitPath, repoPath string) (bool, error) {
	command, err := repositoryGitCommand(ctx, gitPath, repoPath, nil, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false, err
	}
	output, err := command.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 && bytes.Contains(output, []byte("not a git repository")) {
			return false, nil
		}
		return false, fmt.Errorf("run git rev-parse --is-inside-work-tree: %w", err)
	}
	return strings.TrimSpace(string(output)) == "true", nil
}

func repositoryCurrentCommit(ctx context.Context, gitPath, repoPath string) (string, bool, error) {
	command, err := repositoryGitCommand(ctx, gitPath, repoPath, nil, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return "", false, err
	}
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("run git rev-parse --verify --quiet HEAD: %w", err)
	}
	return strings.TrimSpace(string(output)), true, nil
}

func repositoryChangedFiles(ctx context.Context, gitPath, repoPath string, configuredDrivers []string, hasHead bool) ([]string, error) {
	filterArgs := repositoryFilterOverrides(configuredDrivers)
	groups := make([][]string, 0, 3)
	if hasHead {
		changed, err := repositoryGitPaths(ctx, gitPath, repoPath, filterArgs, "diff", "--no-ext-diff", "--no-textconv", "HEAD", "--name-only", "-z", "--")
		if err != nil {
			return nil, err
		}
		groups = append(groups, changed)
	} else {
		for _, args := range [][]string{
			{"diff", "--no-ext-diff", "--no-textconv", "--cached", "--name-only", "-z", "--"},
			{"diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z", "--"},
		} {
			changed, err := repositoryGitPaths(ctx, gitPath, repoPath, filterArgs, args...)
			if err != nil {
				return nil, err
			}
			groups = append(groups, changed)
		}
	}
	untracked, err := repositoryGitPaths(ctx, gitPath, repoPath, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	return mergeRepositoryGitPaths(append(groups, untracked)...), nil
}

func repositoryActiveFilterDrivers(ctx context.Context, gitPath, repoPath string, configuredDrivers []string) ([]RepositoryGitFilter, error) {
	if len(configuredDrivers) == 0 {
		return nil, nil
	}
	output, err := repositoryGitCheckAttrOutput(ctx, gitPath, repoPath)
	if err != nil {
		return nil, err
	}
	configured := repositoryConfiguredDriverSet(configuredDrivers)
	filters, err := parseRepositoryGitFilters(output, configured)
	if err != nil {
		return nil, err
	}
	active, err := repositoryResolveActiveFilters(ctx, gitPath, repoPath, filters)
	if err != nil {
		return nil, err
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].Path == active[j].Path {
			return active[i].Driver < active[j].Driver
		}
		return active[i].Path < active[j].Path
	})
	return active, nil
}

func repositoryGitCheckAttrOutput(ctx context.Context, gitPath, repoPath string) ([]byte, error) {
	trackedPaths, err := repositoryGitPaths(ctx, gitPath, repoPath, nil, "ls-files", "--cached", "-z")
	if err != nil || len(trackedPaths) == 0 {
		return nil, err
	}
	command, err := repositoryGitCommand(ctx, gitPath, repoPath, nil, "check-attr", "--stdin", "-z", "--all")
	if err != nil {
		return nil, err
	}
	command.Stdin = strings.NewReader(strings.Join(trackedPaths, "\x00") + "\x00")
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run git check-attr --stdin -z --all: %w", err)
	}
	return output, nil
}

func repositoryConfiguredDriverSet(configuredDrivers []string) map[string]struct{} {
	configured := make(map[string]struct{}, len(configuredDrivers))
	for _, driver := range configuredDrivers {
		configured[driver] = struct{}{}
	}
	return configured
}

func repositoryResolveActiveFilters(ctx context.Context, gitPath, repoPath string, filters []RepositoryGitFilter) ([]RepositoryGitFilter, error) {
	active := filters[:0]
	for _, filter := range filters {
		if !repositoryGitAttributeStateDriver(filter.Driver) {
			active = append(active, filter)
			continue
		}
		usesDriver, err := repositoryPathUsesNamedFilterDriver(ctx, gitPath, repoPath, filter)
		if err != nil {
			return nil, err
		}
		if usesDriver {
			active = append(active, filter)
		}
	}
	return active, nil
}

func repositoryExecutableFilterDrivers(ctx context.Context, gitPath, repoPath string) ([]string, error) {
	command, err := repositoryGitCommand(ctx, gitPath, repoPath, nil, "config", "--null", "--includes", "--get-regexp", `^filter\..*\.(clean|process)$`)
	if err != nil {
		return nil, err
	}
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("read executable git filter configuration: %w", err)
	}

	effective := make(map[string]string)
	for _, record := range parseRepositoryNULFields(output) {
		key, value, ok := strings.Cut(record, "\n")
		_, validKey := repositoryGitFilterDriverFromConfigKey(key)
		if !ok || !validKey {
			return nil, fmt.Errorf("parse executable git filter configuration: unexpected record %q", record)
		}
		effective[key] = strings.TrimSpace(value)
	}
	configured := make(map[string]struct{}, len(effective))
	for key, command := range effective {
		if command != "" {
			driver, _ := repositoryGitFilterDriverFromConfigKey(key)
			configured[driver] = struct{}{}
		}
	}
	drivers := make([]string, 0, len(configured))
	for driver := range configured {
		drivers = append(drivers, driver)
	}
	sort.Strings(drivers)
	return drivers, nil
}

func parseRepositoryGitFilters(output []byte, configured map[string]struct{}) ([]RepositoryGitFilter, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("parse git attributes: missing trailing NUL terminator")
	}
	fields := parseRepositoryNULFields(output)
	if len(fields)%3 != 0 {
		return nil, fmt.Errorf("parse git attributes: incomplete attribute triplet")
	}
	filters := make([]RepositoryGitFilter, 0, len(fields)/3)
	for index := 0; index < len(fields); index += 3 {
		driver := strings.TrimSpace(fields[index+2])
		if fields[index+1] != "filter" {
			continue
		}
		if _, ok := configured[driver]; ok {
			filters = append(filters, RepositoryGitFilter{Path: fields[index], Driver: driver})
		}
	}
	return filters, nil
}

func repositoryPathUsesNamedFilterDriver(ctx context.Context, gitPath, repoPath string, filter RepositoryGitFilter) (bool, error) {
	probeCommand := "git hash-object --stdin --" + repositoryGitFilterProbeMarker
	configArgs := []string{
		"-c", fmt.Sprintf("filter.%s.clean=%s", filter.Driver, probeCommand),
		"-c", fmt.Sprintf("filter.%s.process=%s", filter.Driver, probeCommand),
		"-c", fmt.Sprintf("filter.%s.required=true", filter.Driver),
	}
	command, err := repositoryGitCommand(ctx, gitPath, repoPath, configArgs, "hash-object", "--path="+filter.Path, "--stdin")
	if err != nil {
		return false, err
	}
	command.Stdin = strings.NewReader("lopper-repository-filter-probe\n")
	output, probeErr := command.CombinedOutput()
	if bytes.Contains(output, []byte(repositoryGitFilterProbeMarker)) {
		return true, nil
	}
	if probeErr != nil {
		return false, fmt.Errorf("probe git filter driver %q for %q: %w", filter.Driver, filter.Path, probeErr)
	}
	return false, nil
}

func repositoryGitAttributeStateDriver(driver string) bool {
	return driver == "set" || driver == "unset" || driver == "unspecified"
}

func repositoryGitFilterDriverFromConfigKey(key string) (string, bool) {
	lowerKey := strings.ToLower(key)
	if !strings.HasPrefix(lowerKey, "filter.") {
		return "", false
	}
	for _, suffix := range []string{".clean", ".process"} {
		if strings.HasSuffix(lowerKey, suffix) && len(key) > len("filter.")+len(suffix) {
			return key[len("filter.") : len(key)-len(suffix)], true
		}
	}
	return "", false
}

func repositoryFilterOverrides(drivers []string) []string {
	overrides := make([]string, 0, len(drivers)*6)
	for _, driver := range drivers {
		overrides = append(overrides, "-c", fmt.Sprintf("filter.%s.clean=", driver), "-c", fmt.Sprintf("filter.%s.process=", driver), "-c", fmt.Sprintf("filter.%s.required=false", driver))
	}
	return overrides
}

func repositoryGitPaths(ctx context.Context, gitPath, repoPath string, configArgs []string, args ...string) ([]string, error) {
	command, err := repositoryGitCommand(ctx, gitPath, repoPath, configArgs, args...)
	if err != nil {
		return nil, err
	}
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run git %s: %w", strings.Join(args, " "), err)
	}
	return parseRepositoryNULFields(output), nil
}

func repositoryGitCommand(ctx context.Context, gitPath, repoPath string, configArgs []string, args ...string) (*exec.Cmd, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	commandArgs := append(gitexec.SafeConfigArgs(), configArgs...)
	commandArgs = append(commandArgs, "-C", repoPath)
	commandArgs = append(commandArgs, args...)
	command, err := gitexec.CommandContext(ctx, gitPath, commandArgs...)
	if err != nil {
		return nil, err
	}
	command.Env = append(gitexec.SanitizedEnv(), "LC_ALL=C")
	return command, nil
}

func parseRepositoryNULFields(output []byte) []string {
	if len(output) == 0 {
		return nil
	}
	fields := bytes.Split(output, []byte{0})
	if len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	parsed := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) > 0 {
			parsed = append(parsed, string(field))
		}
	}
	return parsed
}

func mergeRepositoryGitPaths(groups ...[]string) []string {
	paths := make(map[string]struct{})
	for _, group := range groups {
		for _, path := range group {
			paths[path] = struct{}{}
		}
	}
	merged := make([]string, 0, len(paths))
	for path := range paths {
		merged = append(merged, path)
	}
	sort.Strings(merged)
	return merged
}
