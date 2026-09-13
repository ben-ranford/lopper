package swift

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = filepath.Clean(shared.DefaultRepoPath(repoPath))
	detection := language.Detection{}
	roots := make(map[string]struct{})
	rootSignals := []shared.RootSignal{
		{Name: packageManifestName, Confidence: 60},
		{Name: packageResolvedName, Confidence: 25},
		{Name: podManifestName, Confidence: 60},
		{Name: podLockName, Confidence: 25},
	}
	if err := shared.ApplyRootSignals(repoPath, rootSignals, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	rootCarthage, err := applyRootCarthageSignals(ctx, repoPath, &detection, roots)
	if err != nil {
		return language.Detection{}, err
	}

	if err := walkSwiftDetection(ctx, repoPath, &detection, roots, rootCarthage); err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

type rootCarthagePreflight struct {
	confidence   int
	corroborated bool
}

func applyRootCarthageSignals(ctx context.Context, repoPath string, detection *language.Detection, roots map[string]struct{}) (rootCarthagePreflight, error) {
	confidence, err := rootCarthageDetectionConfidence(repoPath)
	if err != nil || confidence == 0 {
		return rootCarthagePreflight{}, err
	}
	corroborated, _, err := probeSwiftSourceWithinRoot(ctx, repoPath, maxRootCarthageSourceTraversalEntries)
	if err != nil || !corroborated {
		return rootCarthagePreflight{confidence: confidence}, err
	}
	detection.Matched = true
	detection.Confidence += confidence
	roots[filepath.Clean(repoPath)] = struct{}{}
	return rootCarthagePreflight{confidence: confidence, corroborated: true}, nil
}

func rootCarthageDetectionConfidence(repoPath string) (int, error) {
	confidence := 0
	for _, signal := range []shared.RootSignal{
		{Name: carthageManifestName, Confidence: 60},
		{Name: carthageResolvedName, Confidence: 25},
	} {
		info, err := os.Lstat(filepath.Join(repoPath, signal.Name))
		if err == nil {
			if info.Mode().IsRegular() {
				confidence += signal.Confidence
			}
			continue
		}
		if !os.IsNotExist(err) {
			return 0, err
		}
	}
	return confidence, nil
}

func probeSwiftSourceWithinRoot(ctx context.Context, repoPath string, maxEntries int) (found bool, entriesSeen int, err error) {
	if err := contextError(ctx); err != nil {
		return false, 0, err
	}
	root, err := openSwiftSourceProbeRoot(repoPath)
	if isIgnorableOptionalCarthageProbeOpenError(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	return probeSwiftSourceWithinTrustedRoot(ctx, root, ".", maxEntries)
}

// openSwiftSourceProbeRoot permits the caller-selected repository root to be
// an alias while keeping every entry below that resolved root no-follow.
func openSwiftSourceProbeRoot(repoPath string) (safeio.Root, error) {
	resolvedPath, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		return nil, err
	}
	return safeio.OpenRootNoFollow(resolvedPath)
}

func discoverSwiftSourceCandidatesWithinLimit(ctx context.Context, root safeio.Root, directoryPath string, maxEntries int) (directories []string, entriesSeen int, found bool, err error) {
	directory, err := safeio.OpenPinnedDirectory(root, directoryPath)
	if err != nil {
		return nil, 0, false, err
	}
	defer func() {
		err = joinSwiftCarthageProbeCleanupError(err, directory.Close())
	}()

	for entriesSeen < maxEntries {
		if err := contextError(ctx); err != nil {
			return nil, entriesSeen, false, err
		}
		entries, complete, readErr := readRootCarthageSourceBatch(directory, maxEntries-entriesSeen)
		entriesSeen += len(entries)
		if readErr != nil {
			return nil, entriesSeen, false, readErr
		}
		for _, entry := range entries {
			regular, infoErr := isRegularSwiftSource(entry)
			if infoErr != nil {
				return nil, entriesSeen, false, infoErr
			}
			if regular {
				return nil, entriesSeen, true, nil
			}
			if entry.IsDir() && entry.Type()&fs.ModeSymlink == 0 && !shouldSkipDir(entry.Name()) {
				directories = append(directories, filepath.Join(directoryPath, entry.Name()))
			}
		}
		if complete {
			break
		}
	}
	slices.Sort(directories)
	return directories, entriesSeen, false, nil
}

func findSwiftSourceWithinRootDirectory(ctx context.Context, root safeio.Root, directoryPath string, maxEntries int) (found bool, entriesSeen int, err error) {
	return findSwiftSourceWithinRootDirectories(ctx, root, []string{directoryPath}, maxEntries)
}

// findSwiftSourceWithinRootDirectories preserves each top-level candidate's
// initial fair share, then resumes unfinished candidates without replaying
// their prior reads.
func findSwiftSourceWithinRootDirectories(ctx context.Context, root safeio.Root, directories []string, maxEntries int) (found bool, entriesSeen int, returnErr error) {
	return findSwiftSourceWithinResumableSubtrees(ctx, root, newRootCarthageSourceSubtrees(directories), maxEntries)
}

// rootCarthageSourceSharedHandleLimit normally caps retained cursor handles.
// A single path that intrinsically costs more is admitted exclusively so a
// supported nested root is not rejected merely for its ancestor depth.
const rootCarthageSourceSharedHandleLimit = 16

// rootCarthageSourceHandleBudget bounds the file descriptors retained by
// suspended cursors. Opening a k-component path pins k handles: k-1 ancestor
// roots plus the leaf directory.
type rootCarthageSourceHandleBudget struct {
	used int
}

func (b *rootCarthageSourceHandleBudget) acquire(cost int) bool {
	if cost > rootCarthageSourceSharedHandleLimit {
		if b.used != 0 {
			return false
		}
		b.used = cost
		return true
	}
	if b.used+cost > rootCarthageSourceSharedHandleLimit {
		return false
	}
	b.used += cost
	return true
}

func (b *rootCarthageSourceHandleBudget) release(cost int) {
	b.used -= cost
}

type rootCarthageSourceSubtree struct {
	current *rootCarthageSourceCursor
	pending []rootCarthageSourceDirectory
}

func newRootCarthageSourceSubtrees(directories []string) []*rootCarthageSourceSubtree {
	subtrees := make([]*rootCarthageSourceSubtree, 0, len(directories))
	for _, directory := range directories {
		subtrees = append(subtrees, &rootCarthageSourceSubtree{pending: []rootCarthageSourceDirectory{{path: directory, depth: 1}}})
	}
	return subtrees
}

func findSwiftSourceWithinResumableSubtrees(ctx context.Context, root safeio.Root, subtrees []*rootCarthageSourceSubtree, maxEntries int) (found bool, entriesSeen int, returnErr error) {
	handles := &rootCarthageSourceHandleBudget{}
	defer func() {
		returnErr = errors.Join(returnErr, closeRootCarthageSourceSubtrees(subtrees))
	}()

	remaining := maxEntries
	unfinished := make([]*rootCarthageSourceSubtree, 0, len(subtrees))
	blocked := make([]*rootCarthageSourceSubtree, 0, len(subtrees))
	for index, subtree := range subtrees {
		if remaining == 0 {
			break
		}
		quota := max(1, remaining/(len(subtrees)-index))
		found, entries, complete, wasBlocked, err := advanceRootCarthageSourceSubtree(ctx, root, subtree, handles, quota)
		entriesSeen += entries
		remaining -= entries
		if err != nil || found {
			return found, entriesSeen, err
		}
		if wasBlocked {
			blocked = append(blocked, subtree)
		} else if !complete {
			unfinished = append(unfinished, subtree)
		}
	}
	return resumeRootCarthageSourceSubtrees(ctx, root, unfinished, blocked, handles, remaining, entriesSeen)
}

func resumeRootCarthageSourceSubtrees(ctx context.Context, root safeio.Root, subtrees, blockedSubtrees []*rootCarthageSourceSubtree, handles *rootCarthageSourceHandleBudget, remaining, entriesSeen int) (found bool, totalEntries int, returnErr error) {
	state := newRootCarthageSourceResumeState(subtrees, blockedSubtrees)
	for state.hasWork() && remaining > 0 {
		subtree := state.next()
		if subtree == nil {
			return false, entriesSeen, errors.New("swift source traversal handle scheduler could not drain a blocked subtree")
		}
		options := rootCarthageSourceResumeOptions{
			drainForLease: state.drainForLease,
			remaining:     remaining,
			budget:        max(1, remaining/(state.queueLength()+1)),
		}
		found, entries, complete, wasBlocked, drained, err := advanceResumedRootCarthageSourceSubtree(ctx, root, subtree, handles, options)
		if drained {
			state.drainForLease = false
		}
		totalEntries = entriesSeen + entries
		remaining -= entries
		if err != nil || found {
			return found, totalEntries, err
		}
		entriesSeen = totalEntries
		if wasBlocked {
			state.waitForLease(subtree)
		} else if !complete {
			state.queue = append(state.queue, subtree)
		}
	}
	return false, entriesSeen, nil
}

type rootCarthageSourceResumeState struct {
	queue         []*rootCarthageSourceSubtree
	leaseWaiter   *rootCarthageSourceSubtree
	drainForLease bool
}

func newRootCarthageSourceResumeState(subtrees, blockedSubtrees []*rootCarthageSourceSubtree) rootCarthageSourceResumeState {
	state := rootCarthageSourceResumeState{queue: append([]*rootCarthageSourceSubtree(nil), subtrees...)}
	if len(blockedSubtrees) == 0 {
		return state
	}
	state.leaseWaiter = blockedSubtrees[0]
	state.drainForLease = true
	state.queue = append(state.queue, blockedSubtrees[1:]...)
	return state
}

func (s *rootCarthageSourceResumeState) hasWork() bool {
	return len(s.queue) > 0 || s.leaseWaiter != nil
}

func (s *rootCarthageSourceResumeState) queueLength() int {
	return len(s.queue)
}

func (s *rootCarthageSourceResumeState) next() *rootCarthageSourceSubtree {
	if s.drainForLease {
		for index, subtree := range s.queue {
			if subtree.current != nil && subtree.current.directory != nil {
				s.queue = append(s.queue[:index], s.queue[index+1:]...)
				return subtree
			}
		}
		return nil
	}
	if s.leaseWaiter != nil {
		subtree := s.leaseWaiter
		s.leaseWaiter = nil
		return subtree
	}
	if len(s.queue) == 0 {
		return nil
	}
	subtree := s.queue[0]
	s.queue = s.queue[1:]
	return subtree
}

func (s *rootCarthageSourceResumeState) waitForLease(subtree *rootCarthageSourceSubtree) {
	s.drainForLease = true
	if s.leaseWaiter == nil {
		s.leaseWaiter = subtree
		return
	}
	s.queue = append(s.queue, subtree)
}

type rootCarthageSourceResumeOptions struct {
	drainForLease bool
	remaining     int
	budget        int
}

func advanceResumedRootCarthageSourceSubtree(ctx context.Context, root safeio.Root, subtree *rootCarthageSourceSubtree, handles *rootCarthageSourceHandleBudget, options rootCarthageSourceResumeOptions) (found bool, entriesSeen int, complete bool, blocked bool, drained bool, returnErr error) {
	if options.drainForLease && subtree.current != nil && subtree.current.directory != nil {
		found, entriesSeen, complete, returnErr = drainRootCarthageSourceCursor(ctx, root, subtree, handles, options.remaining)
		return found, entriesSeen, complete, false, true, returnErr
	}
	found, entriesSeen, complete, blocked, returnErr = advanceRootCarthageSourceSubtree(ctx, root, subtree, handles, options.budget)
	return found, entriesSeen, complete, blocked, false, returnErr
}

// drainRootCarthageSourceCursor completes only the already-open cursor. It is
// used to free a lease for a blocked logical subtree without discarding or
// replaying any candidate.
func drainRootCarthageSourceCursor(ctx context.Context, root safeio.Root, subtree *rootCarthageSourceSubtree, handles *rootCarthageSourceHandleBudget, maxEntries int) (found bool, entriesSeen int, complete bool, returnErr error) {
	for subtree.current != nil && entriesSeen < maxEntries {
		found, cursorComplete, blocked, entries, err := advanceRootCarthageSourceCursor(ctx, root, subtree.current, handles, maxEntries-entriesSeen)
		entriesSeen += entries
		if err != nil || found {
			return found, entriesSeen, true, err
		}
		if blocked {
			return false, entriesSeen, false, errors.New("open swift source cursor lost its handle lease")
		}
		if !cursorComplete {
			continue
		}
		subtree.pending = appendRootCarthageSourceDirectories(subtree.pending, subtree.current.children)
		subtree.current = nil
	}
	return false, entriesSeen, subtree.current == nil && len(subtree.pending) == 0, nil
}

type rootCarthageSourceCursor struct {
	candidate rootCarthageSourceDirectory
	directory safeio.ReadDirFile
	children  []rootCarthageSourceDirectory
	handles   *rootCarthageSourceHandleBudget
	cost      int
}

func advanceRootCarthageSourceSubtree(ctx context.Context, root safeio.Root, subtree *rootCarthageSourceSubtree, handles *rootCarthageSourceHandleBudget, maxEntries int) (found bool, entriesSeen int, complete bool, blocked bool, returnErr error) {
	for entriesSeen < maxEntries {
		if err := contextError(ctx); err != nil {
			return false, entriesSeen, true, false, closeRootCarthageSourceSubtree(subtree, err)
		}
		if prepareRootCarthageSourceCursor(subtree) {
			return false, entriesSeen, true, false, nil
		}
		found, cursorComplete, cursorBlocked, entries, err := advanceRootCarthageSourceCursor(ctx, root, subtree.current, handles, maxEntries-entriesSeen)
		entriesSeen += entries
		if err != nil || found {
			return found, entriesSeen, true, false, err
		}
		if cursorBlocked {
			return false, entriesSeen, false, true, nil
		}
		if cursorComplete {
			subtree.pending = appendRootCarthageSourceDirectories(subtree.pending, subtree.current.children)
			subtree.current = nil
		}
	}
	return false, entriesSeen, subtree.current == nil && len(subtree.pending) == 0, false, nil
}

func prepareRootCarthageSourceCursor(subtree *rootCarthageSourceSubtree) bool {
	if subtree.current != nil {
		return false
	}
	if len(subtree.pending) == 0 {
		return true
	}
	subtree.current = &rootCarthageSourceCursor{candidate: subtree.pending[0]}
	subtree.pending = subtree.pending[1:]
	return false
}

func advanceRootCarthageSourceCursor(ctx context.Context, root safeio.Root, cursor *rootCarthageSourceCursor, handles *rootCarthageSourceHandleBudget, remaining int) (found, complete, blocked bool, entriesSeen int, returnErr error) {
	opened, err := openRootCarthageSourceCursor(root, cursor, handles)
	if err != nil {
		return false, true, false, 0, err
	}
	if !opened {
		return false, false, true, 0, nil
	}
	if err := contextError(ctx); err != nil {
		return false, true, false, 0, closeRootCarthageSourceCursor(cursor, err)
	}
	entries, complete, err := readRootCarthageSourceBatch(cursor.directory, remaining)
	entriesSeen = len(entries)
	if err != nil {
		return false, true, false, entriesSeen, closeRootCarthageSourceCursor(cursor, err)
	}
	found, err = collectRootCarthageSourceCandidates(entries, cursor.candidate, &cursor.children)
	if err != nil {
		return false, true, false, entriesSeen, closeRootCarthageSourceCursor(cursor, err)
	}
	if found {
		return true, true, false, entriesSeen, closeRootCarthageSourceCursor(cursor, nil)
	}
	if complete || len(entries) == 0 {
		err := error(nil)
		if !complete {
			err = io.ErrNoProgress
		}
		return false, true, false, entriesSeen, closeRootCarthageSourceCursor(cursor, err)
	}
	return false, false, false, entriesSeen, nil
}

func openRootCarthageSourceCursor(root safeio.Root, cursor *rootCarthageSourceCursor, handles *rootCarthageSourceHandleBudget) (bool, error) {
	if cursor.directory != nil {
		return true, nil
	}
	cursor.handles = handles
	cursor.cost = rootCarthageSourcePathCost(cursor.candidate.path)
	if !handles.acquire(cursor.cost) {
		cursor.handles = nil
		cursor.cost = 0
		return false, nil
	}
	directory, err := safeio.OpenPinnedDirectory(root, cursor.candidate.path)
	if err != nil {
		handles.release(cursor.cost)
		cursor.handles = nil
		cursor.cost = 0
		return false, err
	}
	cursor.directory = directory
	return true, nil
}

func rootCarthageSourcePathCost(path string) int {
	cleanPath := filepath.Clean(path)
	if cleanPath == "." {
		return 1
	}
	return len(strings.Split(cleanPath, string(os.PathSeparator)))
}

func closeRootCarthageSourceCursor(cursor *rootCarthageSourceCursor, err error) error {
	if cursor.directory == nil {
		return err
	}
	closeErr := cursor.directory.Close()
	cursor.directory = nil
	if cursor.handles != nil {
		cursor.handles.release(cursor.cost)
		cursor.handles = nil
		cursor.cost = 0
	}
	return joinSwiftCarthageProbeCleanupError(err, closeErr)
}

type swiftCarthageProbeCleanupError struct {
	err error
}

func (e *swiftCarthageProbeCleanupError) Error() string {
	return "swift source probe cleanup failed: " + e.err.Error()
}

func (e *swiftCarthageProbeCleanupError) Unwrap() error {
	return e.err
}

func joinSwiftCarthageProbeCleanupError(err, cleanupErr error) error {
	if cleanupErr == nil {
		return err
	}
	return errors.Join(err, &swiftCarthageProbeCleanupError{err: cleanupErr})
}

func appendRootCarthageSourceDirectories(queue, children []rootCarthageSourceDirectory) []rootCarthageSourceDirectory {
	slices.SortFunc(children, func(left, right rootCarthageSourceDirectory) int {
		return strings.Compare(left.path, right.path)
	})
	return append(queue, children...)
}

func closeRootCarthageSourceSubtree(subtree *rootCarthageSourceSubtree, err error) error {
	if subtree.current == nil {
		return err
	}
	return closeRootCarthageSourceCursor(subtree.current, err)
}

func closeRootCarthageSourceSubtrees(subtrees []*rootCarthageSourceSubtree) error {
	var err error
	for _, subtree := range subtrees {
		err = closeRootCarthageSourceSubtree(subtree, err)
	}
	return err
}

func readRootCarthageSourceBatch(directory safeio.ReadDirFile, remaining int) ([]fs.DirEntry, bool, error) {
	entries, err := directory.ReadDir(min(rootCarthageSourceReadBatchSize, remaining))
	if err == nil {
		return entries, false, nil
	}
	if shared.IsPureSentinelError(err, io.EOF) {
		return entries, true, nil
	}
	return entries, false, err
}

func collectRootCarthageSourceCandidates(entries []fs.DirEntry, candidate rootCarthageSourceDirectory, candidates *[]rootCarthageSourceDirectory) (bool, error) {
	for _, entry := range entries {
		regular, err := isRegularSwiftSource(entry)
		if err != nil {
			return false, err
		}
		if regular {
			return true, nil
		}
		if canDescendRootCarthageSourceCandidate(entry, candidate.depth) {
			*candidates = append(*candidates, rootCarthageSourceDirectory{path: filepath.Join(candidate.path, entry.Name()), depth: candidate.depth + 1})
		}
	}
	return false, nil
}

func canDescendRootCarthageSourceCandidate(entry fs.DirEntry, depth int) bool {
	return entry.IsDir() && entry.Type()&fs.ModeSymlink == 0 && !shouldSkipDir(entry.Name()) && depth < maxRootCarthageSourceDepth
}

type rootCarthageSourceDirectory struct {
	path  string
	depth int
}

func isRegularSwiftSource(entry fs.DirEntry) (bool, error) {
	if !strings.EqualFold(filepath.Ext(entry.Name()), swiftSourceExtension) {
		return false, nil
	}
	return isRegularNonSymlink(entry)
}

func isRegularNonSymlink(entry fs.DirEntry) (bool, error) {
	if entry.Type()&fs.ModeSymlink != 0 {
		return false, nil
	}
	info, err := entry.Info()
	if shared.IsPureSentinelError(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

func walkSwiftDetection(ctx context.Context, repoPath string, detection *language.Detection, roots map[string]struct{}, rootCarthage rootCarthagePreflight) error {
	carthageRoots := make(map[string]int)
	swiftDirectories := make(map[string]struct{})
	if rootCarthage.corroborated {
		swiftDirectories[filepath.Clean(repoPath)] = struct{}{}
	} else if rootCarthage.confidence > 0 {
		carthageRoots[filepath.Clean(repoPath)] = rootCarthage.confidence
	}
	resolvedRepoPath, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		return err
	}
	err = shared.WalkRepoFiles(ctx, resolvedRepoPath, maxDetectFiles, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		requestedPath, pathErr := swiftDetectionPathForRequestedRoot(repoPath, resolvedRepoPath, path)
		if pathErr != nil {
			return pathErr
		}
		path = requestedPath
		confidence, regular, entryErr := carthageDetectionConfidence(entry)
		if entryErr != nil {
			return entryErr
		}
		if confidence > 0 {
			carthageRoots[filepath.Dir(path)] += confidence
		}
		if regular {
			recordSwiftSourceDirectories(repoPath, path, swiftDirectories)
		}
		return recordSwiftDetectionEntry(path, entry, regular, detection, roots)
	})
	if err != nil {
		return err
	}
	return applyCarthageDetectionRoots(ctx, repoPath, detection, roots, carthageRoots, swiftDirectories)
}

func applyCarthageDetectionRoots(ctx context.Context, repoPath string, detection *language.Detection, roots map[string]struct{}, carthageRoots map[string]int, swiftDirectories map[string]struct{}) (returnErr error) {
	candidates := collectUncorroboratedCarthageRoots(repoPath, detection, roots, carthageRoots, swiftDirectories)
	if len(candidates) == 0 {
		return nil
	}
	trustedRoot, err := openSwiftSourceProbeRoot(repoPath)
	if isIgnorableOptionalCarthageProbeOpenError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, trustedRoot.Close())
	}()

	remaining := maxNestedCarthageSourceTraversalEntries
	for index, root := range candidates {
		if remaining == 0 {
			break
		}
		budget := max(1, remaining/(len(candidates)-index))
		relativeRoot, ok := nestedCarthageRootRelativePath(repoPath, root)
		if !ok {
			continue
		}
		found, entries, err := probeSwiftSourceWithinTrustedRoot(ctx, trustedRoot, relativeRoot, budget)
		remaining -= entries
		if isIgnorableNestedCarthageProbeError(err) {
			continue
		}
		if err != nil {
			return err
		}
		if found {
			applyCarthageDetectionRoot(root, carthageRoots[root], detection, roots)
		}
	}
	return nil
}

func isIgnorableNestedCarthageProbeError(err error) bool {
	return !isSwiftCarthageProbeCleanupError(err) && shared.IsPureSentinelError(err, safeio.ErrTargetPathSymlink, fs.ErrNotExist)
}

func swiftDetectionPathForRequestedRoot(repoPath, resolvedRepoPath, path string) (string, error) {
	relativePath, err := filepath.Rel(resolvedRepoPath, path)
	if err != nil {
		return "", err
	}
	return filepath.Join(repoPath, relativePath), nil
}

func collectUncorroboratedCarthageRoots(repoPath string, detection *language.Detection, roots map[string]struct{}, carthageRoots map[string]int, swiftDirectories map[string]struct{}) []string {
	candidates := make([]string, 0, len(carthageRoots))
	for root, confidence := range carthageRoots {
		if _, corroborated := swiftDirectories[root]; corroborated {
			applyCarthageDetectionRoot(root, confidence, detection, roots)
			continue
		}
		if filepath.Clean(root) != filepath.Clean(repoPath) {
			candidates = append(candidates, root)
		}
	}
	slices.Sort(candidates)
	return candidates
}

func nestedCarthageRootRelativePath(repoPath, candidate string) (string, bool) {
	relativePath, err := filepath.Rel(repoPath, candidate)
	if err != nil || relativePath == "." || filepath.IsAbs(relativePath) {
		return "", false
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return relativePath, true
}

func probeSwiftSourceWithinTrustedRoot(ctx context.Context, root safeio.Root, relativePath string, maxEntries int) (bool, int, error) {
	directories, rootEntries, found, err := discoverSwiftSourceCandidatesWithinLimit(ctx, root, relativePath, maxEntries)
	if isIgnorableCarthageProbeResourceError(err) {
		return false, rootEntries, nil
	}
	if err != nil || found {
		return found, rootEntries, err
	}

	found, entries, err := findSwiftSourceWithinRootDirectories(ctx, root, directories, maxEntries-rootEntries)
	if isIgnorableCarthageProbeResourceError(err) {
		return false, rootEntries + entries, nil
	}
	return found, rootEntries + entries, err
}

func isIgnorableCarthageProbeResourceError(err error) bool {
	return !isSwiftCarthageProbeCleanupError(err) && shared.IsPureSentinelError(err, syscall.EMFILE)
}

func isIgnorableOptionalCarthageProbeOpenError(err error) bool {
	var joined safeio.UnwrapAller
	return !errors.As(err, &joined) && isIgnorableCarthageProbeResourceError(err)
}

func isSwiftCarthageProbeCleanupError(err error) bool {
	var cleanupErr *swiftCarthageProbeCleanupError
	return errors.As(err, &cleanupErr)
}

func applyCarthageDetectionRoot(root string, confidence int, detection *language.Detection, roots map[string]struct{}) {
	detection.Matched = true
	detection.Confidence += confidence
	roots[root] = struct{}{}
}

func carthageDetectionConfidence(entry fs.DirEntry) (int, bool, error) {
	switch strings.ToLower(entry.Name()) {
	case strings.ToLower(carthageManifestName), strings.ToLower(carthageResolvedName):
	default:
		regular, err := isRegularSwiftSource(entry)
		return 0, regular, err
	}
	regular, err := isRegularNonSymlink(entry)
	if err != nil || !regular {
		return 0, false, err
	}
	return 10, false, nil
}

func recordSwiftSourceDirectories(repoPath, path string, directories map[string]struct{}) {
	root := filepath.Clean(repoPath)
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		directories[dir] = struct{}{}
		if dir == root || dir == filepath.Dir(dir) {
			return
		}
	}
}

func detectSwiftEntry(ctx context.Context, path string, entry fs.DirEntry, detection *language.Detection, roots map[string]struct{}, visited *int) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if entry.IsDir() {
		return maybeSkipSwiftDir(entry.Name())
	}

	(*visited)++
	if *visited > maxDetectFiles {
		return fs.SkipAll
	}
	regular, err := isRegularSwiftSource(entry)
	if err != nil {
		return err
	}
	return recordSwiftDetectionEntry(path, entry, regular, detection, roots)
}

func recordSwiftDetectionEntry(path string, entry fs.DirEntry, regularSwift bool, detection *language.Detection, roots map[string]struct{}) error {
	switch strings.ToLower(entry.Name()) {
	case strings.ToLower(packageManifestName), strings.ToLower(packageResolvedName), strings.ToLower(podManifestName), strings.ToLower(podLockName):
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	}
	if regularSwift {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
}
