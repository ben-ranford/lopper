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

const lockfileDriftManifestReadLimit int64 = 1 << 20

type lockfileDirSnapshot struct {
	repoPath string
	path     string
	relDir   string
	files    map[string]fs.FileInfo
}

type lockfileManifestIO struct {
	readFileUnder      func(rootDir, targetPath string) ([]byte, error)
	readFileUnderLimit func(rootDir, targetPath string, limit int64) ([]byte, error)
}

type lockfileManifestIOContextKey struct{}

type cachedManifestRead struct {
	content []byte
	err     error
}

type lockfileManifestCache struct {
	snapshot lockfileDirSnapshot
	io       lockfileManifestIO
	reads    map[string]cachedManifestRead
}

type lockfilePreparedManifestRef struct {
	name    string
	relPath string
}

type lockfilePreparedLockfileRef struct {
	name    string
	relPath string
}

type lockfilePreparedManifestChange struct {
	rule      lockfileRule
	relDir    string
	manifests []lockfilePreparedManifestRef
	lockfiles []lockfilePreparedLockfileRef
}

type lockfilePreparedRuleReplay struct {
	rule      lockfileRule
	manifests []string
	lockfiles []string
}

type lockfilePreparedRule struct {
	replay          *lockfilePreparedRuleReplay
	manifestChange  *lockfilePreparedManifestChange
	manifestReadErr error
}

type lockfilePreparedDir struct {
	repoPath string
	path     string
	relDir   string
	rules    []lockfilePreparedRule
}

type lockfilePreparedScan struct {
	dirs       []lockfilePreparedDir
	readErrors lockfileManifestReadErrors
}

type lockfileManifestReadErrorRecord struct {
	key string
	err error
}

type lockfileManifestReadErrors struct {
	records []lockfileManifestReadErrorRecord
	seen    map[string]struct{}
}

type lockfileDriftEvaluationEvent struct {
	finding         lockfileDriftFinding
	hasFinding      bool
	manifestReadErr error
}

type lockfileDirEvaluation struct {
	events     []lockfileDriftEvaluationEvent
	readErrors lockfileManifestReadErrors
	fatalErr   error
}

func (e *lockfileManifestReadErrors) add(snapshot lockfileDirSnapshot, rule lockfileRule, err error) bool {
	return e.addRecord(lockfileManifestReadErrorRecord{
		key: lockfileManifestReadErrorKey(snapshot, rule),
		err: err,
	})
}

func (e *lockfileManifestReadErrors) addRecord(record lockfileManifestReadErrorRecord) bool {
	if e.seen == nil {
		e.seen = make(map[string]struct{})
	}
	if _, exists := e.seen[record.key]; exists {
		return false
	}
	e.seen[record.key] = struct{}{}
	e.records = append(e.records, record)
	return true
}

func (e *lockfileManifestReadErrors) merge(other lockfileManifestReadErrors) {
	for _, record := range other.records {
		e.addRecord(record)
	}
}

func (e *lockfileManifestReadErrors) joined() error {
	errs := make([]error, 0, len(e.records))
	for _, record := range e.records {
		errs = append(errs, record.err)
	}
	return errors.Join(errs...)
}

func lockfileManifestReadErrorKey(snapshot lockfileDirSnapshot, rule lockfileRule) string {
	manifestName := rule.manifest
	if manifests := findRuleManifests(snapshot.files, rule); len(manifests) > 0 {
		manifestName = manifests[0]
	}
	return filepath.Clean(filepath.Join(snapshot.path, manifestName))
}

func defaultLockfileManifestIO() lockfileManifestIO {
	return lockfileManifestIO{
		readFileUnder:      safeio.ReadFileUnder,
		readFileUnderLimit: safeio.ReadFileUnderLimit,
	}
}

func lockfileManifestIOWithDefaults(io lockfileManifestIO) lockfileManifestIO {
	defaults := defaultLockfileManifestIO()
	if io.readFileUnder == nil {
		io.readFileUnder = defaults.readFileUnder
	}
	if io.readFileUnderLimit == nil {
		io.readFileUnderLimit = defaults.readFileUnderLimit
	}
	return io
}

func lockfileManifestIOFromContext(ctx context.Context) lockfileManifestIO {
	if ctx == nil {
		return defaultLockfileManifestIO()
	}
	io, _ := ctx.Value(lockfileManifestIOContextKey{}).(lockfileManifestIO)
	return lockfileManifestIOWithDefaults(io)
}

func (r *lockfileDriftResult) appendEvaluation(evaluation lockfileDirEvaluation) {
	for _, event := range evaluation.events {
		if event.hasFinding {
			warning := buildLockfileDriftWarning(event.finding)
			r.findings = append(r.findings, warning)
			r.orderedWarnings = append(r.orderedWarnings, warning)
			continue
		}
		if event.manifestReadErr != nil {
			r.orderedWarnings = append(r.orderedWarnings, oversizedLockfileDriftWarning(event.manifestReadErr))
		}
	}
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

const lockfileGitBatchSnapshots = 128

type lockfileGitSnapshotBatch struct {
	snapshots          []lockfileDirSnapshot
	candidatePaths     []string
	candidatePathBytes int
	seenCandidates     map[string]struct{}
}

type lockfileFailFastBatchScanner struct {
	repoPath   string
	rules      []lockfileRule
	warnings   []string
	batch      lockfileGitSnapshotBatch
	manifestIO lockfileManifestIO
}

func (b *lockfileGitSnapshotBatch) wouldOverflow(candidatePaths []string) bool {
	if len(b.snapshots) == 0 {
		return false
	}
	additionalPaths := 0
	additionalBytes := 0
	for _, path := range candidatePaths {
		if _, seen := b.seenCandidates[path]; seen {
			continue
		}
		additionalPaths++
		additionalBytes += gitPathspecArgBytes(path)
	}
	return len(b.snapshots) >= lockfileGitBatchSnapshots ||
		len(b.candidatePaths)+additionalPaths > gitPathspecBatchPaths ||
		b.candidatePathBytes+additionalBytes > gitPathspecBatchBytes
}

func (b *lockfileGitSnapshotBatch) add(snapshot lockfileDirSnapshot, candidatePaths []string) {
	b.snapshots = append(b.snapshots, snapshot)
	if b.seenCandidates == nil {
		b.seenCandidates = make(map[string]struct{}, len(candidatePaths))
	}
	for _, path := range candidatePaths {
		if _, seen := b.seenCandidates[path]; seen {
			continue
		}
		b.seenCandidates[path] = struct{}{}
		b.candidatePaths = append(b.candidatePaths, path)
		b.candidatePathBytes += gitPathspecArgBytes(path)
	}
}

func (b *lockfileGitSnapshotBatch) take() ([]lockfileDirSnapshot, []string) {
	snapshots := b.snapshots
	candidatePaths := b.candidatePaths
	b.snapshots = nil
	b.candidatePaths = nil
	b.candidatePathBytes = 0
	clear(b.seenCandidates)
	return snapshots, candidatePaths
}

func scanLockfileDrift(ctx context.Context, repoPath string, gitContext lockfileGitContext, stopOnFirst bool, rules []lockfileRule) ([]string, error) {
	result := scanLockfileDriftDetailed(ctx, repoPath, gitContext, stopOnFirst, rules)
	return result.findings, result.err
}

func scanLockfileDriftDetailed(ctx context.Context, repoPath string, gitContext lockfileGitContext, stopOnFirst bool, rules []lockfileRule) lockfileDriftResult {
	if gitContext.preparedScan != nil && !stopOnFirst {
		return scanPreparedLockfileDrift(ctx, gitContext, rules)
	}
	manifestIO := lockfileManifestIOFromContext(ctx)

	result := lockfileDriftResult{
		findings:        make([]string, 0, len(rules)),
		orderedWarnings: make([]string, 0, len(rules)),
	}
	var readErrors lockfileManifestReadErrors
	state := lockfileWalkState{
		repoPath: repoPath,
		visit: func(snapshot lockfileDirSnapshot) error {
			if stopOnFirst {
				warning, found, err := firstLockfileDriftWarning(snapshot, gitContext, rules)
				if err != nil {
					return err
				}
				if !found {
					return nil
				}
				result.findings = append(result.findings, warning)
				result.orderedWarnings = append(result.orderedWarnings, warning)
				return fs.SkipAll
			}

			evaluation := evaluateLockfileDirWithRulesAndCache(snapshot, gitContext, rules, newLockfileManifestCacheWithIO(snapshot, manifestIO))
			result.appendEvaluation(evaluation)
			readErrors.merge(evaluation.readErrors)
			return evaluation.fatalErr
		},
	}
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		return processLockfileDir(ctx, path, entry, walkErr, state)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		result.err = errors.Join(readErrors.joined(), err)
		return result
	}
	result.err = readErrors.joined()
	return result
}

func scanPreparedLockfileDrift(ctx context.Context, gitContext lockfileGitContext, rules []lockfileRule) lockfileDriftResult {
	prepared := gitContext.preparedScan
	manifestIO := lockfileManifestIOFromContext(ctx)
	result := lockfileDriftResult{
		findings:        make([]string, 0, len(rules)),
		orderedWarnings: make([]string, 0, len(rules)),
	}
	for _, dir := range prepared.dirs {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				result.err = errors.Join(prepared.readErrors.joined(), result.err, err)
				return result
			}
		}
		result.appendPreparedDir(dir, gitContext, manifestIO)
		if result.err != nil && !isRecoverableLockfileManifestReadError(result.err) {
			result.err = errors.Join(prepared.readErrors.joined(), result.err)
			return result
		}
	}
	result.err = errors.Join(prepared.readErrors.joined(), result.err)
	return result
}

func scanLockfileDriftStopOnFirst(ctx context.Context, repoPath string, rules []lockfileRule) ([]string, error) {
	warnings := make([]string, 0, 1)
	hasGitContext, err := isGitWorktree(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	if !hasGitContext {
		return scanLockfileDrift(ctx, repoPath, lockfileGitContext{}, true, rules)
	}

	scanner := lockfileFailFastBatchScanner{
		repoPath:   repoPath,
		rules:      rules,
		warnings:   warnings,
		manifestIO: lockfileManifestIOFromContext(ctx),
	}
	return scanner.scan(ctx)
}

func (s *lockfileFailFastBatchScanner) scan(ctx context.Context) ([]string, error) {
	state := lockfileWalkState{
		repoPath: s.repoPath,
		visit: func(snapshot lockfileDirSnapshot) error {
			return s.visit(ctx, snapshot)
		},
	}
	walkErr := filepath.WalkDir(s.repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		return s.handleWalkEntry(ctx, path, entry, walkErr, state)
	})
	return s.result(ctx, walkErr)
}

func (s *lockfileFailFastBatchScanner) visit(ctx context.Context, snapshot lockfileDirSnapshot) error {
	manifestCache := newLockfileManifestCacheWithIO(snapshot, s.manifestIO)
	warning, findingRuleIndex, found, err := firstLockfileDriftWarningWithRuleIndex(snapshot, lockfileGitContext{}, s.rules, manifestCache)
	if err != nil {
		// Empty Git context can miss an earlier manifest-change drift in the same
		// directory. Flush older buffered snapshots before replaying the current one
		// so fail-fast findings still follow source/directory order.
		if flushErr := s.flush(ctx); flushErr != nil {
			return flushErr
		}
		return s.recordFirstSnapshotRuleByRule(ctx, snapshot)
	}
	candidateRules := s.rules
	if found {
		candidateRules = s.rules[:findingRuleIndex]
	}
	candidatePaths, err := lockfileManifestChangeCandidatePathsWithCache(snapshot, candidateRules, manifestCache)
	if err != nil {
		return err
	}
	if found && len(candidatePaths) == 0 {
		if err := s.flush(ctx); err != nil {
			return err
		}
		s.warnings = append(s.warnings, warning)
		return fs.SkipAll
	}
	if len(candidatePaths) == 0 {
		return nil
	}
	if s.batch.wouldOverflow(candidatePaths) {
		if err := s.flush(ctx); err != nil {
			return err
		}
	}
	s.batch.add(snapshot, candidatePaths)
	if found {
		return s.flush(ctx)
	}
	return nil
}

func (s *lockfileFailFastBatchScanner) recordFirst(snapshot lockfileDirSnapshot, gitContext lockfileGitContext) error {
	warning, found, err := firstLockfileDriftWarning(snapshot, gitContext, s.rules)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	s.warnings = append(s.warnings, warning)
	return fs.SkipAll
}

func (s *lockfileFailFastBatchScanner) flush(ctx context.Context) error {
	snapshots, candidatePaths := s.batch.take()
	if len(snapshots) == 0 {
		return nil
	}
	gitContext, err := collectLockfileGitContextForPaths(ctx, s.repoPath, candidatePaths)
	if err != nil {
		var filterErr *lockfileDriftFilterAmbiguityError
		if !errors.As(err, &filterErr) {
			return err
		}
		return s.flushSnapshotsInOrder(ctx, snapshots)
	}
	for _, snapshot := range snapshots {
		if err := s.recordFirst(snapshot, gitContext); err != nil {
			return err
		}
	}
	return nil
}

func (s *lockfileFailFastBatchScanner) flushSnapshotsInOrder(ctx context.Context, snapshots []lockfileDirSnapshot) error {
	for _, snapshot := range snapshots {
		if err := s.recordFirstSnapshotRuleByRule(ctx, snapshot); err != nil {
			return err
		}
	}
	return nil
}

func (s *lockfileFailFastBatchScanner) recordFirstSnapshotRuleByRule(ctx context.Context, snapshot lockfileDirSnapshot) error {
	manifestCache := newLockfileManifestCacheWithIO(snapshot, s.manifestIO)
	for _, rule := range s.rules {
		candidatePaths, err := lockfileManifestChangeCandidatePathsForRule(snapshot, rule, manifestCache)
		if err != nil {
			return err
		}
		gitContext := lockfileGitContext{}
		if len(candidatePaths) > 0 {
			gitContext, err = collectLockfileGitContextForPaths(ctx, s.repoPath, candidatePaths)
			if err != nil {
				return err
			}
		}

		finding, found, err := evaluateLockfileRuleWithCache(snapshot, rule, gitContext, manifestCache)
		if err != nil {
			return err
		}
		if !found {
			continue
		}

		s.warnings = append(s.warnings, buildLockfileDriftWarning(finding))
		return fs.SkipAll
	}
	return nil
}

func (s *lockfileFailFastBatchScanner) handleWalkEntry(ctx context.Context, path string, entry fs.DirEntry, walkErr error, state lockfileWalkState) error {
	if walkErr != nil {
		if flushErr := s.flush(ctx); flushErr != nil {
			return flushErr
		}
		return walkErr
	}
	processErr := processLockfileDir(ctx, path, entry, nil, state)
	if processErr == nil || errors.Is(processErr, fs.SkipDir) || errors.Is(processErr, fs.SkipAll) {
		return processErr
	}
	if flushErr := s.flush(ctx); flushErr != nil {
		return flushErr
	}
	return processErr
}

func (s *lockfileFailFastBatchScanner) result(ctx context.Context, walkErr error) ([]string, error) {
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return nil, walkErr
	}
	if flushErr := s.flush(ctx); flushErr != nil {
		if errors.Is(flushErr, fs.SkipAll) {
			return s.warnings, nil
		}
		return nil, flushErr
	}
	return s.warnings, nil
}

func firstLockfileDriftWarning(snapshot lockfileDirSnapshot, gitContext lockfileGitContext, rules []lockfileRule) (string, bool, error) {
	warning, _, found, err := firstLockfileDriftWarningWithRuleIndex(snapshot, gitContext, rules, newLockfileManifestCache(snapshot))
	return warning, found, err
}

func firstLockfileDriftWarningWithRuleIndex(snapshot lockfileDirSnapshot, gitContext lockfileGitContext, rules []lockfileRule, manifestCache *lockfileManifestCache) (string, int, bool, error) {
	for ruleIndex, rule := range rules {
		finding, ok, err := evaluateLockfileRuleWithCache(snapshot, rule, gitContext, manifestCache)
		if err != nil {
			return "", 0, false, err
		}
		if ok {
			return buildLockfileDriftWarning(finding), ruleIndex, true, nil
		}
	}
	return "", 0, false, nil
}

func collectLockfileManifestChangeCandidatePaths(ctx context.Context, repoPath string, rules []lockfileRule) ([]string, error) {
	_, candidates, err := prepareLockfileManifestChangeCandidates(ctx, repoPath, rules)
	return candidates, err
}

func prepareLockfileManifestChangeCandidates(ctx context.Context, repoPath string, rules []lockfileRule) (*lockfilePreparedScan, []string, error) {
	return prepareLockfileManifestChangeCandidatesWithIO(ctx, repoPath, rules, lockfileManifestIOFromContext(ctx))
}

func prepareLockfileManifestChangeCandidatesWithIO(ctx context.Context, repoPath string, rules []lockfileRule, manifestIO lockfileManifestIO) (*lockfilePreparedScan, []string, error) {
	prepared := &lockfilePreparedScan{
		dirs: make([]lockfilePreparedDir, 0),
	}
	candidates := make([]string, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	state := lockfileWalkState{
		repoPath: repoPath,
		visit: func(snapshot lockfileDirSnapshot) error {
			dir, paths, err := prepareLockfileDir(snapshot, rules, manifestIO, &prepared.readErrors)
			if len(dir.rules) > 0 {
				prepared.dirs = append(prepared.dirs, dir)
			}
			candidates = appendUniqueLockfilePaths(candidates, seen, paths)
			return err
		},
	}
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		return processLockfileDir(ctx, path, entry, walkErr, state)
	})
	sort.Strings(candidates)
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return prepared, candidates, errors.Join(prepared.readErrors.joined(), err)
	}
	return prepared, candidates, prepared.readErrors.joined()
}

func lockfileManifestChangeCandidatePaths(snapshot lockfileDirSnapshot, rules []lockfileRule) ([]string, error) {
	return lockfileManifestChangeCandidatePathsWithCache(snapshot, rules, newLockfileManifestCache(snapshot))
}

func lockfileManifestChangeCandidatePathsWithCache(snapshot lockfileDirSnapshot, rules []lockfileRule, manifestCache *lockfileManifestCache) ([]string, error) {
	var readErrors lockfileManifestReadErrors
	candidates, err := lockfileManifestChangeCandidatePathsWithReadErrors(snapshot, rules, manifestCache, &readErrors)
	return candidates, errors.Join(readErrors.joined(), err)
}

func lockfileManifestChangeCandidatePathsWithReadErrors(snapshot lockfileDirSnapshot, rules []lockfileRule, manifestCache *lockfileManifestCache, readErrors *lockfileManifestReadErrors) ([]string, error) {
	candidates := make([]string, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		ruleCandidates, err := lockfileManifestChangeCandidatePathsForRule(snapshot, rule, manifestCache)
		if err != nil {
			if isRecoverableLockfileManifestReadError(err) {
				readErrors.add(snapshot, rule, err)
				continue
			}
			sort.Strings(candidates)
			return candidates, err
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
		io:       defaultLockfileManifestIO(),
	}
}

func readLockfileManifest(rootDir, targetPath string) ([]byte, error) {
	return readLockfileManifestWithIO(defaultLockfileManifestIO(), rootDir, targetPath)
}

func newLockfileManifestCacheWithIO(snapshot lockfileDirSnapshot, io lockfileManifestIO) *lockfileManifestCache {
	return &lockfileManifestCache{
		snapshot: snapshot,
		io:       lockfileManifestIOWithDefaults(io),
	}
}

func readLockfileManifestWithIO(io lockfileManifestIO, rootDir, targetPath string) ([]byte, error) {
	return lockfileManifestIOWithDefaults(io).readFileUnderLimit(rootDir, targetPath, lockfileDriftManifestReadLimit)
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
	content, err := readLockfileManifestWithIO(c.io, c.snapshot.repoPath, filepath.Join(c.snapshot.path, manifestName))
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
		content, err = readLockfileManifest(snapshot.repoPath, filepath.Join(snapshot.path, manifestName))
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
	skip, decided, err := shouldSkipMissingLockfileBeforeManifestContentInspection(snapshot, rule, manifestName, cache)
	if err != nil || decided {
		return skip, err
	}

	content, err := readManifestForLockfileDrift(snapshot, manifestName, "", cache)
	if err != nil {
		return false, err
	}

	return shouldSkipMissingLockfileFromManifestContent(manifestName, content), nil
}

func shouldSkipMissingLockfileBeforeManifestContentInspection(snapshot lockfileDirSnapshot, rule lockfileRule, manifestName string, cache *lockfileManifestCache) (bool, bool, error) {
	sectionNeedle := manifestMatcherNeedle(rule)
	if sectionNeedle != "" {
		content, err := readManifestForLockfileDrift(snapshot, manifestName, "", cache)
		if err != nil {
			return false, false, err
		}
		if !pyprojectSectionNeedleMatchesContent(sectionNeedle, content) {
			return true, true, nil
		}
		return false, false, nil
	}

	if rule.manifestMatcher != nil {
		matched, err := rule.manifestMatcher(snapshot.repoPath, snapshot.path)
		if err != nil {
			return false, false, err
		}
		if !matched {
			return true, true, nil
		}
		return false, false, nil
	}

	if !manifestNeedsContentInspection(manifestName) {
		return false, true, nil
	}

	return false, false, nil
}

func shouldSkipMissingLockfileFromManifestContent(manifestName string, content []byte) bool {
	text := string(content)
	switch manifestName {
	case "go.mod":
		// go.sum is only generated when a module has external dependencies.
		// A stdlib-only module has go.mod but no go.sum and that is valid.
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "require") {
				return false
			}
		}
		return true
	case "Cargo.toml":
		// Library crates conventionally omit Cargo.lock from version control.
		// Only warn for binary crates (those with a [[bin]] section).
		return !strings.Contains(text, "[[bin]]")
	}
	return false
}

func manifestNeedsContentInspection(manifestName string) bool {
	switch manifestName {
	case "go.mod", "Cargo.toml":
		return true
	default:
		return false
	}
}

func evaluateLockfileDir(snapshot lockfileDirSnapshot, gitContext lockfileGitContext) ([]lockfileDriftFinding, error) {
	return evaluateLockfileDirWithRules(snapshot, gitContext, activeLockfileRules(featureflags.Set{}))
}

func evaluateLockfileDirWithRules(snapshot lockfileDirSnapshot, gitContext lockfileGitContext, rules []lockfileRule) ([]lockfileDriftFinding, error) {
	evaluation := evaluateLockfileDirWithRulesAndCache(snapshot, gitContext, rules, newLockfileManifestCache(snapshot))
	findings := make([]lockfileDriftFinding, 0, len(rules))
	for _, event := range evaluation.events {
		if event.hasFinding {
			findings = append(findings, event.finding)
		}
	}
	return findings, errors.Join(evaluation.readErrors.joined(), evaluation.fatalErr)
}

func evaluateLockfileDirWithRulesAndCache(snapshot lockfileDirSnapshot, gitContext lockfileGitContext, rules []lockfileRule, manifestCache *lockfileManifestCache) lockfileDirEvaluation {
	evaluation := lockfileDirEvaluation{
		events: make([]lockfileDriftEvaluationEvent, 0, len(rules)),
	}
	for _, rule := range rules {
		finding, ok, err := evaluateLockfileRuleWithCache(snapshot, rule, gitContext, manifestCache)
		if err != nil {
			if isRecoverableLockfileManifestReadError(err) {
				if evaluation.readErrors.add(snapshot, rule, err) {
					evaluation.events = append(evaluation.events, lockfileDriftEvaluationEvent{
						manifestReadErr: err,
					})
				}
				continue
			}
			evaluation.fatalErr = err
			return evaluation
		}
		if !ok {
			continue
		}
		evaluation.events = append(evaluation.events, lockfileDriftEvaluationEvent{
			finding:    finding,
			hasFinding: true,
		})
	}
	return evaluation
}

func (r *lockfileDriftResult) appendPreparedDir(dir lockfilePreparedDir, gitContext lockfileGitContext, manifestIO lockfileManifestIO) {
	snapshot := lockfileDirSnapshot{
		repoPath: dir.repoPath,
		path:     dir.path,
		relDir:   dir.relDir,
	}
	cache := newLockfileManifestCacheWithIO(snapshot, manifestIO)
	for _, rule := range dir.rules {
		if !r.appendPreparedRule(dir, rule, gitContext, cache) {
			return
		}
	}
}

func (r *lockfileDriftResult) appendPreparedRule(dir lockfilePreparedDir, rule lockfilePreparedRule, gitContext lockfileGitContext, cache *lockfileManifestCache) bool {
	if rule.manifestReadErr != nil {
		r.orderedWarnings = append(r.orderedWarnings, oversizedLockfileDriftWarning(rule.manifestReadErr))
		return true
	}
	if rule.manifestChange == nil {
		return r.appendPreparedReplayRule(dir, rule, gitContext, cache)
	}
	finding, found := evaluatePreparedManifestChange(*rule.manifestChange, gitContext)
	if !found {
		return true
	}
	r.appendPreparedFinding(finding)
	return true
}

func (r *lockfileDriftResult) appendPreparedReplayRule(dir lockfilePreparedDir, rule lockfilePreparedRule, gitContext lockfileGitContext, cache *lockfileManifestCache) bool {
	finding, found, err := evaluatePreparedReplayRule(dir, rule, gitContext, cache)
	if err != nil {
		if isRecoverableLockfileManifestReadError(err) {
			r.orderedWarnings = append(r.orderedWarnings, oversizedLockfileDriftWarning(err))
			r.err = errors.Join(r.err, err)
			return true
		}
		r.err = errors.Join(r.err, err)
		return false
	}
	if !found {
		return true
	}
	r.appendPreparedFinding(finding)
	return true
}

func (r *lockfileDriftResult) appendPreparedFinding(finding lockfileDriftFinding) {
	warning := buildLockfileDriftWarning(finding)
	r.findings = append(r.findings, warning)
	r.orderedWarnings = append(r.orderedWarnings, warning)
}

func prepareLockfileDir(snapshot lockfileDirSnapshot, rules []lockfileRule, manifestIO lockfileManifestIO, readErrors *lockfileManifestReadErrors) (lockfilePreparedDir, []string, error) {
	dir := lockfilePreparedDir{
		repoPath: snapshot.repoPath,
		path:     snapshot.path,
		relDir:   snapshot.relDir,
		rules:    make([]lockfilePreparedRule, 0, len(rules)),
	}
	cache := newLockfileManifestCacheWithIO(snapshot, manifestIO)
	candidates := make([]string, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		preparedRule, ruleCandidates, err := prepareLockfileRule(snapshot, rule, cache, readErrors)
		if err != nil {
			sort.Strings(candidates)
			return dir, candidates, err
		}
		if shouldRetainPreparedLockfileRule(preparedRule, ruleCandidates) {
			dir.rules = append(dir.rules, preparedRule)
		}
		candidates = appendUniqueLockfilePaths(candidates, seen, ruleCandidates)
	}
	sort.Strings(candidates)
	return dir, candidates, nil
}

func shouldRetainPreparedLockfileRule(rule lockfilePreparedRule, candidatePaths []string) bool {
	return rule.replay != nil || rule.manifestChange != nil || rule.manifestReadErr != nil || len(candidatePaths) > 0
}

func prepareLockfileRule(snapshot lockfileDirSnapshot, rule lockfileRule, cache *lockfileManifestCache, readErrors *lockfileManifestReadErrors) (lockfilePreparedRule, []string, error) {
	manifests := findRuleManifests(snapshot.files, rule)
	hasManifest := len(manifests) > 0
	manifestName := rule.manifest
	if hasManifest {
		manifestName = manifests[0]
	}
	lockfiles := findRuleLockfiles(snapshot.files, rule.lockfiles)
	lockfiles, err := findDistributedRuleLockfiles(snapshot, rule, manifests, lockfiles)
	if err != nil {
		return lockfilePreparedRule{}, nil, err
	}

	_, handled, err := evaluateMissingOrStaleLockfileWithManifestAndCache(snapshot, rule, hasManifest, manifestName, lockfiles, cache)
	if err != nil {
		prepared, ok := recoverablePreparedLockfileRule(snapshot, rule, err, readErrors)
		if ok {
			return prepared, nil, nil
		}
		return lockfilePreparedRule{}, nil, err
	}
	if handled {
		return preparedReplayLockfileRule(rule, manifests, lockfiles), nil, nil
	}
	if !hasManifest && len(lockfiles) == 0 {
		return lockfilePreparedRule{}, nil, nil
	}

	matchesManifest, err := manifestMatchesRuleWithCache(snapshot, rule, manifestName, cache)
	if err != nil {
		prepared, ok := recoverablePreparedLockfileRule(snapshot, rule, err, readErrors)
		if ok {
			return prepared, nil, nil
		}
		return lockfilePreparedRule{}, nil, err
	}
	if !matchesManifest && len(lockfiles) > 0 {
		return preparedReplayLockfileRule(rule, manifests, lockfiles), nil, nil
	}
	if !hasManifest || len(lockfiles) == 0 || !matchesManifest {
		return lockfilePreparedRule{}, nil, nil
	}

	return lockfilePreparedRule{
		manifestChange: prepareManifestChange(snapshot, rule, manifests, lockfiles),
	}, relativeLockfileCandidatePaths(snapshot, manifests, lockfiles), nil
}

func recoverablePreparedLockfileRule(snapshot lockfileDirSnapshot, rule lockfileRule, err error, readErrors *lockfileManifestReadErrors) (lockfilePreparedRule, bool) {
	if !isRecoverableLockfileManifestReadError(err) {
		return lockfilePreparedRule{}, false
	}
	prepared := lockfilePreparedRule{}
	if readErrors != nil && readErrors.add(snapshot, rule, err) {
		prepared.manifestReadErr = err
	}
	return prepared, true
}

func preparedReplayLockfileRule(rule lockfileRule, manifests []string, lockfiles []presentLockfile) lockfilePreparedRule {
	return lockfilePreparedRule{
		replay: &lockfilePreparedRuleReplay{
			rule:      rule,
			manifests: append([]string(nil), manifests...),
			lockfiles: preparedLockfileNames(lockfiles),
		},
	}
}

func prepareManifestChange(snapshot lockfileDirSnapshot, rule lockfileRule, manifests []string, lockfiles []presentLockfile) *lockfilePreparedManifestChange {
	prepared := &lockfilePreparedManifestChange{
		rule:      rule,
		relDir:    snapshot.relDir,
		manifests: make([]lockfilePreparedManifestRef, 0, len(manifests)),
		lockfiles: make([]lockfilePreparedLockfileRef, 0, len(lockfiles)),
	}
	for _, manifest := range manifests {
		prepared.manifests = append(prepared.manifests, lockfilePreparedManifestRef{
			name:    manifest,
			relPath: relativeFilePath(snapshot.repoPath, snapshot.path, manifest),
		})
	}
	for _, lockfile := range lockfiles {
		prepared.lockfiles = append(prepared.lockfiles, lockfilePreparedLockfileRef{
			name:    lockfile.name,
			relPath: relativeFilePath(snapshot.repoPath, snapshot.path, lockfile.name),
		})
	}
	return prepared
}

func preparedLockfileNames(lockfiles []presentLockfile) []string {
	names := make([]string, 0, len(lockfiles))
	for _, lockfile := range lockfiles {
		names = append(names, lockfile.name)
	}
	return names
}

func evaluatePreparedManifestChange(prepared lockfilePreparedManifestChange, gitContext lockfileGitContext) (lockfileDriftFinding, bool) {
	if !gitContext.hasGitContext || len(gitContext.changedFiles) == 0 {
		return lockfileDriftFinding{}, false
	}
	changedManifest := ""
	for _, manifest := range prepared.manifests {
		if isPathChanged(gitContext.changedFiles, manifest.relPath) {
			changedManifest = manifest.name
			break
		}
	}
	if changedManifest == "" {
		return lockfileDriftFinding{}, false
	}
	lockfiles := make([]presentLockfile, 0, len(prepared.lockfiles))
	for _, lockfile := range prepared.lockfiles {
		if isPathChanged(gitContext.changedFiles, lockfile.relPath) {
			return lockfileDriftFinding{}, false
		}
		lockfiles = append(lockfiles, presentLockfile{name: lockfile.name})
	}
	return lockfileDriftFinding{
		kind:      lockfileDriftManifestChange,
		rule:      prepared.rule,
		manifest:  changedManifest,
		relDir:    prepared.relDir,
		lockfiles: lockfiles,
	}, true
}

func evaluatePreparedReplayRule(dir lockfilePreparedDir, rule lockfilePreparedRule, gitContext lockfileGitContext, cache *lockfileManifestCache) (lockfileDriftFinding, bool, error) {
	if rule.replay == nil {
		return lockfileDriftFinding{}, false, nil
	}
	snapshot := lockfileDirSnapshot{
		repoPath: dir.repoPath,
		path:     dir.path,
		relDir:   dir.relDir,
	}
	replay := rule.replay
	hasManifest := len(replay.manifests) > 0
	manifestName := replay.rule.manifest
	if hasManifest {
		manifestName = replay.manifests[0]
	}
	lockfiles := preparedPresentLockfiles(replay.lockfiles)
	finding, handled, err := evaluateMissingOrStaleLockfileWithManifestAndCache(snapshot, replay.rule, hasManifest, manifestName, lockfiles, cache)
	if handled || err != nil {
		return finding, handled, err
	}
	if !hasManifest && len(lockfiles) == 0 {
		return lockfileDriftFinding{}, false, nil
	}
	matchesManifest, err := manifestMatchesRuleWithCache(snapshot, replay.rule, manifestName, cache)
	if err != nil {
		return lockfileDriftFinding{}, false, err
	}
	if !matchesManifest && len(lockfiles) > 0 {
		return staleLockfileFinding(snapshot, replay.rule, lockfiles), true, nil
	}
	return lockfileDriftFinding{}, false, nil
}

func preparedPresentLockfiles(names []string) []presentLockfile {
	lockfiles := make([]presentLockfile, 0, len(names))
	for _, name := range names {
		lockfiles = append(lockfiles, presentLockfile{name: name})
	}
	return lockfiles
}

func isRecoverableLockfileManifestReadError(err error) bool {
	return isPureLockfileManifestReadSizeError(err)
}

func isPureLockfileManifestReadSizeError(err error) bool {
	type Unwrapper interface {
		Unwrap() []error
	}
	stack := []error{err}
	sawLeaf := false
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current == nil {
			return false
		}

		var unwrapper Unwrapper
		if errors.As(current, &unwrapper) {
			children := unwrapper.Unwrap()
			if len(children) == 0 {
				return false
			}
			stack = append(stack, children...)
			continue
		}
		if child := errors.Unwrap(current); child != nil {
			stack = append(stack, child)
			continue
		}

		sawLeaf = true
		if !errors.Is(current, safeio.ErrFileTooLarge) {
			return false
		}
	}
	return sawLeaf
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
	relativeDirPath := relativeDir(repoPath, dir)
	relativePath := filepath.Join(relativeDirPath, name)
	if relativeDirPath == "." {
		relativePath = name
	}
	return filepath.ToSlash(relativePath)
}

func isPathChanged(changedFiles map[string]struct{}, path string) bool {
	_, ok := changedFiles[path]
	return ok
}
