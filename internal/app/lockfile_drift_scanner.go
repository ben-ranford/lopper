package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

var readFileUnderFn = safeio.ReadFileUnder

type lockfileDirSnapshot struct {
	repoPath string
	path     string
	relDir   string
	files    map[string]fs.FileInfo
}

type cachedManifestRead struct {
	content []byte
	err     error
}

type lockfileManifestCache struct {
	snapshot lockfileDirSnapshot
	reads    map[string]cachedManifestRead
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

type lockfileWalkState struct {
	repoPath string
	visit    func(lockfileDirSnapshot) error
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

func scanLockfileDriftStopOnFirst(ctx context.Context, repoPath string, rules []lockfileRule) ([]string, error) {
	warnings := make([]string, 0, 1)
	hasGitContext := isGitWorktree(ctx, repoPath)
	state := lockfileWalkState{
		repoPath: repoPath,
		visit: func(snapshot lockfileDirSnapshot) error {
			gitContext := lockfileGitContext{}
			if hasGitContext {
				candidatePaths, err := lockfileManifestChangeCandidatePaths(snapshot, rules)
				if err != nil {
					return err
				}
				gitContext, err = collectLockfileGitContextForPaths(ctx, repoPath, candidatePaths)
				if err != nil {
					return err
				}
			}
			findings, err := evaluateLockfileDirWithRules(snapshot, gitContext, rules)
			if err != nil {
				return err
			}
			if len(findings) == 0 {
				return nil
			}
			warnings = append(warnings, buildLockfileDriftWarning(findings[0]))
			return fs.SkipAll
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

func collectLockfileManifestChangeCandidatePaths(ctx context.Context, repoPath string, rules []lockfileRule) ([]string, error) {
	candidates := make([]string, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	state := lockfileWalkState{
		repoPath: repoPath,
		visit: func(snapshot lockfileDirSnapshot) error {
			paths, err := lockfileManifestChangeCandidatePaths(snapshot, rules)
			if err != nil {
				return err
			}
			for _, path := range paths {
				if _, ok := seen[path]; ok {
					continue
				}
				seen[path] = struct{}{}
				candidates = append(candidates, path)
			}
			return nil
		},
	}
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		return processLockfileDir(ctx, path, entry, walkErr, state)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, err
	}
	sort.Strings(candidates)
	return candidates, nil
}

func lockfileManifestChangeCandidatePaths(snapshot lockfileDirSnapshot, rules []lockfileRule) ([]string, error) {
	candidates := make([]string, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	manifestCache := newLockfileManifestCache(snapshot)
	for _, rule := range rules {
		ruleCandidates, err := lockfileManifestChangeCandidatePathsForRule(snapshot, rule, manifestCache)
		if err != nil {
			return nil, err
		}
		candidates = appendUniqueLockfilePaths(candidates, seen, ruleCandidates)
	}
	sort.Strings(candidates)
	return candidates, nil
}

func lockfileManifestChangeCandidatePathsForRule(snapshot lockfileDirSnapshot, rule lockfileRule, manifestCache *lockfileManifestCache) ([]string, error) {
	manifests := findRuleManifests(snapshot.files, rule)
	if len(manifests) == 0 {
		return nil, nil
	}
	lockfiles := findRuleLockfiles(snapshot.files, rule.lockfiles)
	lockfiles, err := findDistributedRuleLockfiles(snapshot, rule, manifests, lockfiles)
	if err != nil {
		return nil, err
	}
	if len(lockfiles) == 0 {
		return nil, nil
	}
	matchesManifest, err := manifestMatchesRuleWithCache(snapshot, rule, manifests[0], manifestCache)
	if err != nil {
		return nil, err
	}
	if !matchesManifest {
		return nil, nil
	}
	return relativeLockfileCandidatePaths(snapshot, manifests, lockfiles), nil
}

func relativeLockfileCandidatePaths(snapshot lockfileDirSnapshot, manifests []string, lockfiles []presentLockfile) []string {
	paths := make([]string, 0, len(manifests)+len(lockfiles))
	for _, manifest := range manifests {
		paths = append(paths, relativeFilePath(snapshot.repoPath, snapshot.path, manifest))
	}
	for _, lockfile := range lockfiles {
		paths = append(paths, relativeFilePath(snapshot.repoPath, snapshot.path, lockfile.name))
	}
	return paths
}

func appendUniqueLockfilePaths(candidates []string, seen map[string]struct{}, paths []string) []string {
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		candidates = append(candidates, path)
	}
	return candidates
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

func newLockfileManifestCache(snapshot lockfileDirSnapshot) *lockfileManifestCache {
	return &lockfileManifestCache{
		snapshot: snapshot,
	}
}

func (c *lockfileManifestCache) readManifest(manifestName string) ([]byte, error) {
	if c == nil {
		return nil, errors.New("nil lockfile manifest cache")
	}
	if c.reads != nil {
		cached, ok := c.reads[manifestName]
		if ok {
			return cached.content, cached.err
		}
	}
	content, err := readFileUnderFn(c.snapshot.repoPath, filepath.Join(c.snapshot.path, manifestName))
	if c.reads == nil {
		c.reads = make(map[string]cachedManifestRead)
	}
	c.reads[manifestName] = cachedManifestRead{
		content: content,
		err:     err,
	}
	return content, err
}

func readManifestForLockfileDrift(snapshot lockfileDirSnapshot, manifestName, matcherLabel string, cache *lockfileManifestCache) ([]byte, error) {
	var (
		content []byte
		err     error
	)
	if cache != nil {
		content, err = cache.readManifest(manifestName)
	} else {
		content, err = readFileUnderFn(snapshot.repoPath, filepath.Join(snapshot.path, manifestName))
	}
	if err != nil {
		if strings.TrimSpace(matcherLabel) != "" {
			return nil, fmt.Errorf("read %s for %s lockfile drift detection: %w", manifestName, matcherLabel, err)
		}
		return nil, fmt.Errorf("read %s for lockfile drift detection: %w", manifestName, err)
	}
	return content, nil
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
	return shouldSkipMissingLockfileForManifestWithCache(snapshot, rule, manifestName, nil)
}

func shouldSkipMissingLockfileForManifestWithCache(snapshot lockfileDirSnapshot, rule lockfileRule, manifestName string, cache *lockfileManifestCache) (bool, error) {
	content, err := readManifestForLockfileDrift(snapshot, manifestName, "", cache)
	if err != nil {
		return false, err
	}
	sectionNeedle := manifestMatcherNeedle(rule)
	switch {
	case sectionNeedle != "":
		if !pyprojectSectionNeedleMatchesContent(sectionNeedle, content) {
			return true, nil
		}
	case rule.manifestMatcher != nil:
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
	manifestCache := newLockfileManifestCache(snapshot)
	for _, rule := range rules {
		finding, ok, err := evaluateLockfileRuleWithCache(snapshot, rule, gitContext, manifestCache)
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
	return evaluateLockfileRuleWithCache(snapshot, rule, gitContext, nil)
}

func evaluateLockfileRuleWithCache(snapshot lockfileDirSnapshot, rule lockfileRule, gitContext lockfileGitContext, cache *lockfileManifestCache) (lockfileDriftFinding, bool, error) {
	manifests := findRuleManifests(snapshot.files, rule)
	hasManifest := len(manifests) > 0
	manifestName := rule.manifest
	if hasManifest {
		manifestName = manifests[0]
	}
	lockfiles := findRuleLockfiles(snapshot.files, rule.lockfiles)
	lockfiles, err := findDistributedRuleLockfiles(snapshot, rule, manifests, lockfiles)
	if err != nil {
		return lockfileDriftFinding{}, false, err
	}

	finding, handled, err := evaluateMissingOrStaleLockfileWithManifestAndCache(snapshot, rule, hasManifest, manifestName, lockfiles, cache)
	if handled || err != nil {
		return finding, handled, err
	}
	if !hasManifest && len(lockfiles) == 0 {
		return lockfileDriftFinding{}, false, nil
	}

	matchesManifest, err := manifestMatchesRuleWithCache(snapshot, rule, manifestName, cache)
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
	return evaluateMissingOrStaleLockfileWithManifestAndCache(snapshot, rule, hasManifest, manifestName, lockfiles, nil)
}

func evaluateMissingOrStaleLockfileWithManifestAndCache(snapshot lockfileDirSnapshot, rule lockfileRule, hasManifest bool, manifestName string, lockfiles []presentLockfile, cache *lockfileManifestCache) (lockfileDriftFinding, bool, error) {
	switch {
	case hasManifest && len(lockfiles) == 0:
		skip, err := shouldSkipMissingLockfileForManifestWithCache(snapshot, rule, manifestName, cache)
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

func manifestMatchesRuleWithCache(snapshot lockfileDirSnapshot, rule lockfileRule, manifestName string, cache *lockfileManifestCache) (bool, error) {
	section := strings.TrimSpace(rule.manifestMatcherLabel)
	sectionNeedle := manifestMatcherNeedle(rule)
	if sectionNeedle != "" {
		content, err := readManifestForLockfileDrift(snapshot, manifestName, section, cache)
		if err != nil {
			return false, err
		}
		return pyprojectSectionNeedleMatchesContent(sectionNeedle, content), nil
	}
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

func findDistributedRuleLockfiles(snapshot lockfileDirSnapshot, rule lockfileRule, manifests []string, lockfiles []presentLockfile) ([]presentLockfile, error) {
	if len(lockfiles) > 0 || !isDotnetCentralOnlyRuleManifest(rule, manifests) {
		return lockfiles, nil
	}
	projectLockfiles, err := findDotnetProjectLockfiles(snapshot.path)
	if err != nil {
		return nil, err
	}
	if len(projectLockfiles) == 0 {
		return lockfiles, nil
	}
	return projectLockfiles, nil
}

func isDotnetCentralOnlyRuleManifest(rule lockfileRule, manifests []string) bool {
	if rule.manager != ".NET" {
		return false
	}
	hasCentralManifest := false
	for _, manifest := range manifests {
		lowerName := strings.ToLower(strings.TrimSpace(manifest))
		switch {
		case strings.EqualFold(lowerName, rule.manifest):
			hasCentralManifest = true
		case strings.HasSuffix(lowerName, dotnetCSharpProjectManifestExt), strings.HasSuffix(lowerName, dotnetFSharpProjectManifestExt):
			return false
		}
	}
	return hasCentralManifest
}

func findDotnetProjectLockfiles(rootDir string) ([]presentLockfile, error) {
	rootDir = filepath.Clean(rootDir)
	lockfiles := make([]presentLockfile, 0)
	err := filepath.WalkDir(rootDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != rootDir && shouldSkipLockfileDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "packages.lock.json" {
			return nil
		}

		lockDir := filepath.Dir(path)
		hasProjectManifest, err := dirContainsDotnetProjectManifest(lockDir)
		if err != nil {
			return err
		}
		if !hasProjectManifest {
			return nil
		}

		relPath := filepath.ToSlash(strings.TrimPrefix(path, rootDir+string(filepath.Separator)))
		lockfiles = append(lockfiles, presentLockfile{name: relPath})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(lockfiles, func(i, j int) bool {
		return lockfiles[i].name < lockfiles[j].name
	})
	return lockfiles, nil
}

func dirContainsDotnetProjectManifest(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lowerName := strings.ToLower(entry.Name())
		if strings.HasSuffix(lowerName, dotnetCSharpProjectManifestExt) || strings.HasSuffix(lowerName, dotnetFSharpProjectManifestExt) {
			return true, nil
		}
	}
	return false, nil
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
