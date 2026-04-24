package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

const (
	lockfileDriftWarningPrefix                     = "lockfile drift detected: "
	pyprojectManifestName                          = "pyproject.toml"
	lockfileDriftEcosystemExpansionPreviewFlagName = "lockfile-drift-ecosystem-expansion-preview"
)

var resolveGitBinaryPathFn = gitexec.ResolveBinaryPath
var execGitCommandContextFn = gitexec.CommandContext

type lockfileRule struct {
	manager            string
	manifest           string
	manifestNames      []string
	manifestExts       []string
	manifestLabel      string
	lockfiles          []string
	remedy             string
	previewFeatureFlag string
	manifestMatcher    func(repoPath, dir string) (bool, error)
}

type lockfileGitContext struct {
	changedFiles  map[string]struct{}
	hasGitContext bool
}

type lockfileDirSnapshot struct {
	repoPath string
	path     string
	relDir   string
	files    map[string]fs.FileInfo
}

type lockfileDriftKind uint8

const (
	lockfileDriftMissingLockfile lockfileDriftKind = iota + 1
	lockfileDriftStaleLockfile
	lockfileDriftManifestChange
)

type lockfileDriftFinding struct {
	kind      lockfileDriftKind
	rule      lockfileRule
	manifest  string
	relDir    string
	lockfiles []presentLockfile
}

var lockfileRules = []lockfileRule{
	{manager: "npm", manifest: "package.json", lockfiles: []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb"}, remedy: "run npm install for package-lock.json/npm-shrinkwrap.json, yarn install for yarn.lock, pnpm install for pnpm-lock.yaml, or bun install for bun.lockb; then commit the updated manifest and lockfile"},
	{manager: "Bundler", manifest: "Gemfile", lockfiles: []string{"Gemfile.lock"}, remedy: "run bundle install (or bundle lock) and commit the updated Gemfile and Gemfile.lock"},
	{manager: "Composer", manifest: "composer.json", lockfiles: []string{"composer.lock"}, remedy: "run composer update --lock (or composer install) and commit the updated files"},
	{manager: "Cargo", manifest: "Cargo.toml", lockfiles: []string{"Cargo.lock"}, remedy: "run cargo generate-lockfile (or cargo build) and commit the updated files"},
	{manager: "Go modules", manifest: "go.mod", lockfiles: []string{"go.sum"}, remedy: "run go mod tidy and commit the updated files"},
	{manager: "Pipenv", manifest: "Pipfile", lockfiles: []string{"Pipfile.lock"}, remedy: "run pipenv lock and commit the updated files"},
	{manager: "Poetry", manifest: pyprojectManifestName, manifestLabel: "Poetry configuration in pyproject.toml", lockfiles: []string{"poetry.lock"}, remedy: "run poetry lock and commit the updated files", manifestMatcher: pyprojectSectionMatcher("tool.poetry")},
	{manager: "uv", manifest: pyprojectManifestName, manifestLabel: "uv configuration in pyproject.toml", lockfiles: []string{"uv.lock"}, remedy: "run uv lock and commit the updated files", manifestMatcher: pyprojectSectionMatcher("tool.uv")},
	{
		manager:            ".NET",
		manifest:           "Directory.Packages.props",
		manifestExts:       []string{".csproj", ".fsproj"},
		manifestLabel:      ".NET project manifest (*.csproj, *.fsproj) or Directory.Packages.props",
		lockfiles:          []string{"packages.lock.json"},
		remedy:             "run dotnet restore --use-lock-file (or dotnet restore for existing lock mode) and commit the updated files",
		previewFeatureFlag: lockfileDriftEcosystemExpansionPreviewFlagName,
	},
	{
		manager:            "Dart",
		manifest:           "pubspec.yaml",
		manifestNames:      []string{"pubspec.yml"},
		lockfiles:          []string{"pubspec.lock"},
		remedy:             "run dart pub get (or flutter pub get) and commit the updated files",
		previewFeatureFlag: lockfileDriftEcosystemExpansionPreviewFlagName,
	},
	{
		manager:            "Elixir",
		manifest:           "mix.exs",
		lockfiles:          []string{"mix.lock"},
		remedy:             "run mix deps.get and commit the updated files",
		previewFeatureFlag: lockfileDriftEcosystemExpansionPreviewFlagName,
	},
	{
		manager:            "SwiftPM",
		manifest:           "Package.swift",
		lockfiles:          []string{"Package.resolved"},
		remedy:             "run swift package resolve and commit the updated files",
		previewFeatureFlag: lockfileDriftEcosystemExpansionPreviewFlagName,
	},
}

func evaluateLockfileDriftPolicy(ctx context.Context, repoPath, policy string) ([]string, error) {
	return evaluateLockfileDriftPolicyWithFeatures(ctx, repoPath, policy, featureflags.Set{})
}

func evaluateLockfileDriftPolicyWithFeatures(ctx context.Context, repoPath, policy string, features featureflags.Set) ([]string, error) {
	normalizedPolicy := strings.TrimSpace(policy)
	if normalizedPolicy == "off" {
		return nil, nil
	}
	failMode := normalizedPolicy == "fail"
	driftWarnings, err := detectLockfileDriftWithFeatures(ctx, repoPath, failMode, features)
	if err != nil || len(driftWarnings) == 0 {
		return driftWarnings, err
	}
	if failMode {
		return driftWarnings, formatLockfileDriftError(driftWarnings)
	}
	return driftWarnings, nil
}

func detectLockfileDrift(ctx context.Context, repoPath string, stopOnFirst bool) ([]string, error) {
	return detectLockfileDriftWithFeatures(ctx, repoPath, stopOnFirst, featureflags.Set{})
}

func detectLockfileDriftWithFeatures(ctx context.Context, repoPath string, stopOnFirst bool, features featureflags.Set) ([]string, error) {
	normalizedPath, err := workspace.NormalizeRepoPath(repoPath)
	if err != nil {
		return nil, err
	}
	gitContext, err := collectLockfileGitContext(ctx, normalizedPath)
	if err != nil {
		return nil, err
	}
	return scanLockfileDrift(ctx, normalizedPath, gitContext, stopOnFirst, activeLockfileRules(features))
}

func activeLockfileRules(features featureflags.Set) []lockfileRule {
	active := make([]lockfileRule, 0, len(lockfileRules))
	for _, rule := range lockfileRules {
		previewFlag := strings.TrimSpace(rule.previewFeatureFlag)
		if previewFlag != "" && !features.Enabled(previewFlag) {
			continue
		}
		active = append(active, rule)
	}
	return active
}

func collectLockfileGitContext(ctx context.Context, repoPath string) (lockfileGitContext, error) {
	changedFiles, hasGitContext, err := gitChangedFiles(ctx, repoPath)
	if err != nil {
		return lockfileGitContext{}, err
	}
	return lockfileGitContext{
		changedFiles:  changedFiles,
		hasGitContext: hasGitContext,
	}, nil
}

func scanLockfileDrift(ctx context.Context, repoPath string, gitContext lockfileGitContext, stopOnFirst bool, rules []lockfileRule) ([]string, error) {
	warnings := make([]string, 0, len(rules))
	state := lockfileWalkState{
		repoPath: repoPath,
		visit: func(snapshot lockfileDirSnapshot) error {
			findings, err := evaluateLockfileDirWithRules(snapshot, gitContext, rules)
			if err != nil {
				return err
			}
			if len(findings) == 0 {
				return nil
			}
			if stopOnFirst {
				warnings = append(warnings, buildLockfileDriftWarning(findings[0]))
				return fs.SkipAll
			}
			warnings = append(warnings, buildLockfileDriftWarnings(findings)...)
			return nil
		},
	}
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		return processLockfileDir(ctx, path, entry, walkErr, state)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, err
	}
	return warnings, nil
}

type lockfileWalkState struct {
	repoPath string
	visit    func(lockfileDirSnapshot) error
}

func processLockfileDir(ctx context.Context, path string, entry fs.DirEntry, walkErr error, state lockfileWalkState) error {
	if walkErr != nil {
		return walkErr
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if !entry.IsDir() {
		return nil
	}
	if path != state.repoPath && shouldSkipLockfileDir(entry.Name()) {
		return filepath.SkipDir
	}
	snapshot, err := readLockfileDirSnapshot(state.repoPath, path)
	if err != nil {
		return err
	}
	if state.visit == nil {
		return nil
	}
	return state.visit(snapshot)
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

func readLockfileDirSnapshot(repoPath, dir string) (lockfileDirSnapshot, error) {
	files, err := readDirectoryFiles(dir)
	if err != nil {
		return lockfileDirSnapshot{}, err
	}
	return lockfileDirSnapshot{
		repoPath: repoPath,
		path:     dir,
		relDir:   relativeDir(repoPath, dir),
		files:    files,
	}, nil
}

func shouldSkipMissingLockfile(snapshot lockfileDirSnapshot, rule lockfileRule) (bool, error) {
	manifestNames := findRuleManifests(snapshot.files, rule)
	manifestName := rule.manifest
	if len(manifestNames) > 0 {
		manifestName = manifestNames[0]
	}
	return shouldSkipMissingLockfileForManifest(snapshot, rule, manifestName)
}

func shouldSkipMissingLockfileForManifest(snapshot lockfileDirSnapshot, rule lockfileRule, manifestName string) (bool, error) {
	content, err := safeio.ReadFileUnder(snapshot.repoPath, filepath.Join(snapshot.path, manifestName))
	if err != nil {
		return false, fmt.Errorf("read %s for lockfile drift detection: %w", manifestName, err)
	}
	if rule.manifestMatcher != nil {
		matched, matchErr := rule.manifestMatcher(snapshot.repoPath, snapshot.path)
		if matchErr != nil {
			return false, matchErr
		}
		if !matched {
			return true, nil
		}
	}
	text := string(content)
	switch manifestName {
	case "go.mod":
		// go.sum is only generated when a module has external dependencies.
		// A stdlib-only module has go.mod but no go.sum and that is valid.
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "require") {
				return false, nil
			}
		}
		return true, nil
	case "Cargo.toml":
		// Library crates conventionally omit Cargo.lock from version control.
		// Only warn for binary crates (those with a [[bin]] section).
		return !strings.Contains(text, "[[bin]]"), nil
	}
	return false, nil
}

func evaluateLockfileDir(snapshot lockfileDirSnapshot, gitContext lockfileGitContext) ([]lockfileDriftFinding, error) {
	return evaluateLockfileDirWithRules(snapshot, gitContext, activeLockfileRules(featureflags.Set{}))
}

func evaluateLockfileDirWithRules(snapshot lockfileDirSnapshot, gitContext lockfileGitContext, rules []lockfileRule) ([]lockfileDriftFinding, error) {
	findings := make([]lockfileDriftFinding, 0, len(rules))
	for _, rule := range rules {
		finding, ok, err := evaluateLockfileRule(snapshot, rule, gitContext)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func evaluateLockfileRule(snapshot lockfileDirSnapshot, rule lockfileRule, gitContext lockfileGitContext) (lockfileDriftFinding, bool, error) {
	manifests := findRuleManifests(snapshot.files, rule)
	hasManifest := len(manifests) > 0
	manifestName := rule.manifest
	if hasManifest {
		manifestName = manifests[0]
	}
	lockfiles := findRuleLockfiles(snapshot.files, rule.lockfiles)

	finding, handled, err := evaluateMissingOrStaleLockfileWithManifest(snapshot, rule, hasManifest, manifestName, lockfiles)
	if handled || err != nil {
		return finding, handled, err
	}
	if !hasManifest && len(lockfiles) == 0 {
		return lockfileDriftFinding{}, false, nil
	}

	matchesManifest, err := manifestMatchesRule(snapshot, rule)
	if err != nil {
		return lockfileDriftFinding{}, false, err
	}
	if !matchesManifest && len(lockfiles) > 0 {
		return staleLockfileFinding(snapshot, rule, lockfiles), true, nil
	}

	if !hasManifest || len(lockfiles) == 0 || !matchesManifest {
		return lockfileDriftFinding{}, false, nil
	}
	if !gitContext.hasGitContext || len(gitContext.changedFiles) == 0 {
		return lockfileDriftFinding{}, false, nil
	}
	return evaluateManifestChangeFinding(snapshot, rule, gitContext, lockfiles, manifests)
}

func evaluateMissingOrStaleLockfile(snapshot lockfileDirSnapshot, rule lockfileRule, hasManifest bool, lockfiles []presentLockfile) (lockfileDriftFinding, bool, error) {
	manifestNames := findRuleManifests(snapshot.files, rule)
	manifestName := rule.manifest
	if hasManifest && len(manifestNames) > 0 {
		manifestName = manifestNames[0]
	}
	return evaluateMissingOrStaleLockfileWithManifest(snapshot, rule, hasManifest, manifestName, lockfiles)
}

func evaluateMissingOrStaleLockfileWithManifest(snapshot lockfileDirSnapshot, rule lockfileRule, hasManifest bool, manifestName string, lockfiles []presentLockfile) (lockfileDriftFinding, bool, error) {
	switch {
	case hasManifest && len(lockfiles) == 0:
		skip, err := shouldSkipMissingLockfileForManifest(snapshot, rule, manifestName)
		if err != nil {
			return lockfileDriftFinding{}, false, err
		}
		if skip {
			return lockfileDriftFinding{}, false, nil
		}
		return lockfileDriftFinding{
			kind:     lockfileDriftMissingLockfile,
			rule:     rule,
			manifest: manifestName,
			relDir:   snapshot.relDir,
		}, true, nil
	case !hasManifest && len(lockfiles) > 0:
		return staleLockfileFinding(snapshot, rule, lockfiles), true, nil
	default:
		return lockfileDriftFinding{}, false, nil
	}
}

func manifestMatchesRule(snapshot lockfileDirSnapshot, rule lockfileRule) (bool, error) {
	if rule.manifestMatcher == nil {
		return true, nil
	}
	return rule.manifestMatcher(snapshot.repoPath, snapshot.path)
}

func staleLockfileFinding(snapshot lockfileDirSnapshot, rule lockfileRule, lockfiles []presentLockfile) lockfileDriftFinding {
	return lockfileDriftFinding{
		kind:      lockfileDriftStaleLockfile,
		rule:      rule,
		relDir:    snapshot.relDir,
		lockfiles: lockfiles,
	}
}

func evaluateManifestChangeFinding(snapshot lockfileDirSnapshot, rule lockfileRule, gitContext lockfileGitContext, lockfiles []presentLockfile, manifests []string) (lockfileDriftFinding, bool, error) {
	changedManifest := ""
	for _, manifestName := range manifests {
		manifestPath := relativeFilePath(snapshot.repoPath, snapshot.path, manifestName)
		if isPathChanged(gitContext.changedFiles, manifestPath) {
			changedManifest = manifestName
			break
		}
	}
	if changedManifest == "" {
		return lockfileDriftFinding{}, false, nil
	}
	for _, lockfile := range lockfiles {
		lockfilePath := relativeFilePath(snapshot.repoPath, snapshot.path, lockfile.name)
		if isPathChanged(gitContext.changedFiles, lockfilePath) {
			return lockfileDriftFinding{}, false, nil
		}
	}
	return lockfileDriftFinding{
		kind:     lockfileDriftManifestChange,
		rule:     rule,
		manifest: changedManifest,
		relDir:   snapshot.relDir,
	}, true, nil
}

func buildLockfileDriftWarnings(findings []lockfileDriftFinding) []string {
	if len(findings) == 0 {
		return nil
	}
	warnings := make([]string, 0, len(findings))
	for _, finding := range findings {
		warnings = append(warnings, buildLockfileDriftWarning(finding))
	}
	return warnings
}

func buildLockfileDriftWarning(finding lockfileDriftFinding) string {
	manifest := manifestNameForFinding(finding)
	switch finding.kind {
	case lockfileDriftMissingLockfile:
		return fmt.Sprintf("%s%s in %s: %s exists but no matching lockfile (%s) was found; %s", lockfileDriftWarningPrefix, finding.rule.manager, finding.relDir, manifest, strings.Join(finding.rule.lockfiles, ", "), finding.rule.remedy)
	case lockfileDriftStaleLockfile:
		return fmt.Sprintf("%s%s in %s: %s exists without %s; remove stale lockfile or restore the manifest", lockfileDriftWarningPrefix, finding.rule.manager, finding.relDir, finding.lockfiles[0].name, manifestDescription(finding.rule))
	case lockfileDriftManifestChange:
		return fmt.Sprintf("%s%s in %s: %s changed while no matching lockfile changed; %s", lockfileDriftWarningPrefix, finding.rule.manager, finding.relDir, manifest, finding.rule.remedy)
	default:
		return fmt.Sprintf("%s%s in %s: unable to classify lockfile drift for %s", lockfileDriftWarningPrefix, finding.rule.manager, finding.relDir, manifest)
	}
}

func detectDriftForRule(repoPath, dir string, files map[string]fs.FileInfo, rule lockfileRule, changedFiles map[string]struct{}, hasGitContext bool) ([]string, error) {
	snapshot := lockfileDirSnapshot{
		repoPath: repoPath,
		path:     dir,
		relDir:   relativeDir(repoPath, dir),
		files:    files,
	}
	gitContext := lockfileGitContext{
		changedFiles:  changedFiles,
		hasGitContext: hasGitContext,
	}

	finding, ok, err := evaluateLockfileRule(snapshot, rule, gitContext)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return []string{buildLockfileDriftWarning(finding)}, nil
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

func findRuleManifests(files map[string]fs.FileInfo, rule lockfileRule) []string {
	manifests := make([]string, 0, 1+len(rule.manifestNames))
	seen := make(map[string]struct{})
	manifests = appendManifestIfPresent(manifests, seen, files, rule.manifest)
	for _, name := range rule.manifestNames {
		manifests = appendManifestIfPresent(manifests, seen, files, name)
	}
	for _, name := range findManifestExtMatches(files, rule.manifestExts) {
		manifests = appendManifestIfPresent(manifests, seen, files, name)
	}

	sort.Strings(manifests)
	return manifests
}

func appendManifestIfPresent(manifests []string, seen map[string]struct{}, files map[string]fs.FileInfo, name string) []string {
	if _, exists := seen[name]; exists {
		return manifests
	}
	if _, exists := files[name]; !exists {
		return manifests
	}
	seen[name] = struct{}{}
	return append(manifests, name)
}

func findManifestExtMatches(files map[string]fs.FileInfo, manifestExts []string) []string {
	lowerExts := normalizedManifestExts(manifestExts)
	if len(lowerExts) == 0 {
		return nil
	}

	matches := make([]string, 0, len(files))
	for name := range files {
		if manifestMatchesAnyExt(name, lowerExts) {
			matches = append(matches, name)
		}
	}
	return matches
}

func normalizedManifestExts(manifestExts []string) []string {
	lowerExts := make([]string, 0, len(manifestExts))
	for _, ext := range manifestExts {
		normalizedExt := strings.ToLower(strings.TrimSpace(ext))
		if normalizedExt == "" {
			continue
		}
		lowerExts = append(lowerExts, normalizedExt)
	}
	return lowerExts
}

func manifestMatchesAnyExt(name string, manifestExts []string) bool {
	lowerName := strings.ToLower(name)
	for _, ext := range manifestExts {
		if strings.HasSuffix(lowerName, ext) {
			return true
		}
	}
	return false
}

func manifestNameForFinding(finding lockfileDriftFinding) string {
	if strings.TrimSpace(finding.manifest) != "" {
		return finding.manifest
	}
	return finding.rule.manifest
}

func manifestDescription(rule lockfileRule) string {
	if strings.TrimSpace(rule.manifestLabel) != "" {
		return rule.manifestLabel
	}
	return rule.manifest
}

func pyprojectSectionMatcher(section string) func(repoPath, dir string) (bool, error) {
	needle := "[" + strings.ToLower(strings.TrimSpace(section)) + "]"
	return func(repoPath, dir string) (bool, error) {
		content, err := safeio.ReadFileUnder(repoPath, filepath.Join(dir, pyprojectManifestName))
		if err != nil {
			return false, fmt.Errorf("read %s for %s lockfile drift detection: %w", pyprojectManifestName, section, err)
		}
		return strings.Contains(strings.ToLower(string(content)), needle), nil
	}
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
	command, err := gitCommandContext(ctx, repoPath, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	output, err := command.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "true"
}

func gitTrackedChanges(ctx context.Context, repoPath string) ([]string, error) {
	hasHead, err := gitHasVerifiedHead(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	if hasHead {
		return gitDiffNameOnly(ctx, repoPath, "HEAD")
	}
	// Unborn HEAD: derive tracked changes from staged + working tree diffs.
	staged, err := gitDiffNameOnly(ctx, repoPath, "--cached")
	if err != nil {
		return nil, err
	}
	unstaged, err := gitDiffNameOnly(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	return mergeGitPaths(staged, unstaged), nil
}

func gitHasVerifiedHead(ctx context.Context, repoPath string) (bool, error) {
	command, err := gitCommandContext(ctx, repoPath, "rev-parse", "--verify", "--quiet", "HEAD")
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

func gitDiffNameOnly(ctx context.Context, repoPath string, diffArgs ...string) ([]string, error) {
	args := []string{"-c", "diff.external=", "diff", "--no-ext-diff", "--no-textconv"}
	args = append(args, diffArgs...)
	args = append(args, "--name-only", "--")
	command, err := gitCommandContext(ctx, repoPath, args...)
	if err != nil {
		return nil, err
	}
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

func gitUntrackedFiles(ctx context.Context, repoPath string) ([]string, error) {
	command, err := gitCommandContext(ctx, repoPath, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run git ls-files --others --exclude-standard: %w", err)
	}
	return parseGitOutputLines(output), nil
}

func gitCommandContext(ctx context.Context, repoPath string, args ...string) (*exec.Cmd, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	gitPath, err := resolveGitBinaryPathFn()
	if err != nil {
		return nil, err
	}
	commandArgs := append([]string{"-C", repoPath}, args...)
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

func sanitizedGitEnv() []string {
	return gitexec.SanitizedEnv()
}
