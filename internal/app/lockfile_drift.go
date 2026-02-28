package app

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/workspace"
)

const lockfileDriftWarningPrefix = "lockfile drift detected: "
const safeSystemPath = "PATH=/usr/bin:/bin:/usr/sbin:/sbin"

type lockfileRule struct {
	manager   string
	manifest  string
	lockfiles []string
	remedy    string
}

var lockfileRules = []lockfileRule{
	{manager: "npm", manifest: "package.json", lockfiles: []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb"}, remedy: "run npm install for package-lock.json/npm-shrinkwrap.json, yarn install for yarn.lock, pnpm install for pnpm-lock.yaml, or bun install for bun.lockb; then commit the updated manifest and lockfile"},
	{manager: "Composer", manifest: "composer.json", lockfiles: []string{"composer.lock"}, remedy: "run composer update --lock (or composer install) and commit the updated files"},
	{manager: "Cargo", manifest: "Cargo.toml", lockfiles: []string{"Cargo.lock"}, remedy: "run cargo generate-lockfile (or cargo build) and commit the updated files"},
	{manager: "Go modules", manifest: "go.mod", lockfiles: []string{"go.sum"}, remedy: "run go mod tidy and commit the updated files"},
	{manager: "Pipenv", manifest: "Pipfile", lockfiles: []string{"Pipfile.lock"}, remedy: "run pipenv lock and commit the updated files"},
	{manager: "Poetry", manifest: "pyproject.toml", lockfiles: []string{"poetry.lock"}, remedy: "run poetry lock and commit the updated files"},
}

func evaluateLockfileDriftPolicy(ctx context.Context, repoPath, policy string) ([]string, error) {
	normalizedPolicy := strings.TrimSpace(policy)
	if normalizedPolicy == "off" {
		return nil, nil
	}
	failMode := normalizedPolicy == "fail"
	driftWarnings, err := detectLockfileDrift(ctx, repoPath, failMode)
	if err != nil || len(driftWarnings) == 0 {
		return driftWarnings, err
	}
	if failMode {
		return driftWarnings, formatLockfileDriftError(driftWarnings)
	}
	return driftWarnings, nil
}

func detectLockfileDrift(ctx context.Context, repoPath string, stopOnFirst bool) ([]string, error) {
	normalizedPath, err := workspace.NormalizeRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	changedFiles, hasGitContext, err := gitChangedFiles(ctx, normalizedPath)
	if err != nil {
		return nil, err
	}

	warnings := make([]string, 0, len(lockfileRules))
	err = filepath.WalkDir(normalizedPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if !entry.IsDir() {
			return nil
		}
		if path != normalizedPath && shouldSkipLockfileDir(entry.Name()) {
			return filepath.SkipDir
		}
		fileInfos, readErr := readDirectoryFiles(path)
		if readErr != nil {
			return readErr
		}
		for _, rule := range lockfileRules {
			warnings = append(warnings, detectDriftForRule(normalizedPath, path, fileInfos, rule, changedFiles, hasGitContext)...)
			if stopOnFirst && len(warnings) > 0 {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return nil, err
	}
	return warnings, nil
}

func shouldSkipLockfileDir(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == ".lopper-cache" {
		return true
	}
	return shared.ShouldSkipCommonDir(normalized)
}

func readDirectoryFiles(path string) (map[string]fs.FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	files := make(map[string]fs.FileInfo, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil, fmt.Errorf("read file info for %q in %q: %w", entry.Name(), path, infoErr)
		}
		files[entry.Name()] = info
	}
	return files, nil
}

func detectDriftForRule(repoPath, dir string, files map[string]fs.FileInfo, rule lockfileRule, changedFiles map[string]struct{}, hasGitContext bool) []string {
	_, hasManifest := files[rule.manifest]
	lockfiles := findRuleLockfiles(files, rule.lockfiles)
	relDir := relativeDir(repoPath, dir)

	if hasManifest && len(lockfiles) == 0 {
		return []string{
			fmt.Sprintf("%s%s in %s: %s exists but no matching lockfile (%s) was found; %s", lockfileDriftWarningPrefix, rule.manager, relDir, rule.manifest, strings.Join(rule.lockfiles, ", "), rule.remedy),
		}
	}
	if !hasManifest && len(lockfiles) > 0 {
		return []string{
			fmt.Sprintf("%s%s in %s: %s exists without %s; remove stale lockfile or restore the manifest", lockfileDriftWarningPrefix, rule.manager, relDir, lockfiles[0].name, rule.manifest),
		}
	}
	if !hasManifest || len(lockfiles) == 0 || !hasGitContext || len(changedFiles) == 0 {
		return nil
	}

	manifestPath := relativeFilePath(repoPath, dir, rule.manifest)
	if !isPathChanged(changedFiles, manifestPath) {
		return nil
	}
	for _, lockfile := range lockfiles {
		lockfilePath := relativeFilePath(repoPath, dir, lockfile.name)
		if isPathChanged(changedFiles, lockfilePath) {
			return nil
		}
	}
	return []string{
		fmt.Sprintf("%s%s in %s: %s changed while no matching lockfile changed; %s", lockfileDriftWarningPrefix, rule.manager, relDir, rule.manifest, rule.remedy),
	}
}

type presentLockfile struct {
	name string
	info fs.FileInfo
}

func findRuleLockfiles(files map[string]fs.FileInfo, names []string) []presentLockfile {
	lockfiles := make([]presentLockfile, 0, len(names))
	for _, name := range names {
		info, ok := files[name]
		if !ok {
			continue
		}
		lockfiles = append(lockfiles, presentLockfile{name: name, info: info})
	}
	return lockfiles
}

func relativeDir(repoPath, dir string) string {
	relative, err := filepath.Rel(repoPath, dir)
	if err != nil {
		return dir
	}
	if relative == "." {
		return "."
	}
	return relative
}

func relativeFilePath(repoPath, dir, name string) string {
	return filepath.ToSlash(filepath.Join(relativeDir(repoPath, dir), name))
}

func isPathChanged(changedFiles map[string]struct{}, path string) bool {
	_, ok := changedFiles[path]
	return ok
}

func formatLockfileDriftError(driftWarnings []string) error {
	if len(driftWarnings) == 0 {
		return ErrLockfileDrift
	}
	cleaned := make([]string, 0, len(driftWarnings))
	for _, warning := range driftWarnings {
		cleaned = append(cleaned, strings.TrimPrefix(warning, lockfileDriftWarningPrefix))
	}
	return fmt.Errorf("%w: %s", ErrLockfileDrift, strings.Join(cleaned, "; "))
}

func gitChangedFiles(ctx context.Context, repoPath string) (map[string]struct{}, bool, error) {
	if !isGitWorktree(ctx, repoPath) {
		return nil, false, nil
	}

	changed := map[string]struct{}{}
	tracked, err := gitTrackedChanges(ctx, repoPath)
	if err != nil {
		return nil, true, err
	}
	for _, path := range tracked {
		changed[path] = struct{}{}
	}

	untracked, err := gitUntrackedFiles(ctx, repoPath)
	if err != nil {
		return nil, true, err
	}
	for _, path := range untracked {
		changed[path] = struct{}{}
	}

	return changed, true, nil
}

func isGitWorktree(ctx context.Context, repoPath string) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	// #nosec G204 -- git is executed without a shell and fixed subcommand/flags.
	command := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--is-inside-work-tree")
	command.Env = sanitizedGitEnv()
	output, err := command.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "true"
}

func gitTrackedChanges(ctx context.Context, repoPath string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// #nosec G204 -- git is executed without a shell and fixed subcommand/flags.
	command := exec.CommandContext(ctx, "git", "-C", repoPath, "diff", "--name-only", "HEAD", "--")
	command.Env = sanitizedGitEnv()
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run git diff --name-only HEAD --: %w", err)
	}
	return parseGitOutputLines(output), nil
}

func gitUntrackedFiles(ctx context.Context, repoPath string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// #nosec G204 -- git is executed without a shell and fixed subcommand/flags.
	command := exec.CommandContext(ctx, "git", "-C", repoPath, "ls-files", "--others", "--exclude-standard")
	command.Env = sanitizedGitEnv()
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run git ls-files --others --exclude-standard: %w", err)
	}
	return parseGitOutputLines(output), nil
}

func parseGitOutputLines(output []byte) []string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func sanitizedGitEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_DIR=") ||
			strings.HasPrefix(entry, "GIT_WORK_TREE=") ||
			strings.HasPrefix(entry, "GIT_INDEX_FILE=") ||
			strings.HasPrefix(entry, "PATH=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, safeSystemPath)
	return filtered
}
