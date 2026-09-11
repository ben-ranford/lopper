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

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
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
	roots[repoPath] = struct{}{}
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
		err = errors.Join(err, directory.Close())
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
			if isRegularSwiftSource(entry) {
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
	queue := []rootCarthageSourceDirectory{{path: directoryPath, depth: 1}}
	for len(queue) > 0 && entriesSeen < maxEntries {
		if err := contextError(ctx); err != nil {
			return false, entriesSeen, err
		}
		candidate := queue[0]
		queue = queue[1:]
		if candidate.depth > maxRootCarthageSourceDepth {
			continue
		}
		found, entries, err := walkCarthageSwiftSourceDirectory(ctx, root, candidate, &queue, maxEntries-entriesSeen)
		entriesSeen += entries
		if err != nil || found {
			return found, entriesSeen, err
		}
	}
	return false, entriesSeen, nil
}

func walkCarthageSwiftSourceDirectory(ctx context.Context, root safeio.Root, candidate rootCarthageSourceDirectory, queue *[]rootCarthageSourceDirectory, maxEntries int) (found bool, entriesSeen int, err error) {
	directory, err := safeio.OpenPinnedDirectory(root, candidate.path)
	if err != nil {
		return false, 0, err
	}
	defer func() {
		err = errors.Join(err, directory.Close())
	}()

	children := make([]rootCarthageSourceDirectory, 0)
	for entriesSeen < maxEntries {
		if err := contextError(ctx); err != nil {
			return false, entriesSeen, err
		}
		entries, complete, err := readRootCarthageSourceBatch(directory, maxEntries-entriesSeen)
		entriesSeen += len(entries)
		if err != nil {
			return false, entriesSeen, err
		}
		if collectRootCarthageSourceCandidates(entries, candidate, &children) {
			return true, entriesSeen, nil
		}
		if complete {
			break
		}
	}
	enqueueSortedRootCarthageSourceCandidates(queue, children)
	return false, entriesSeen, nil
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

func collectRootCarthageSourceCandidates(entries []fs.DirEntry, candidate rootCarthageSourceDirectory, candidates *[]rootCarthageSourceDirectory) bool {
	for _, entry := range entries {
		if isRegularSwiftSource(entry) {
			return true
		}
		if canDescendRootCarthageSourceCandidate(entry, candidate.depth) {
			*candidates = append(*candidates, rootCarthageSourceDirectory{path: filepath.Join(candidate.path, entry.Name()), depth: candidate.depth + 1})
		}
	}
	return false
}

func enqueueSortedRootCarthageSourceCandidates(queue *[]rootCarthageSourceDirectory, candidates []rootCarthageSourceDirectory) {
	slices.SortFunc(candidates, func(left, right rootCarthageSourceDirectory) int {
		return strings.Compare(left.path, right.path)
	})
	*queue = append(*queue, candidates...)
}

func canDescendRootCarthageSourceCandidate(entry fs.DirEntry, depth int) bool {
	return entry.IsDir() && entry.Type()&fs.ModeSymlink == 0 && !shouldSkipDir(entry.Name()) && depth < maxRootCarthageSourceDepth
}

type rootCarthageSourceDirectory struct {
	path  string
	depth int
}

func isRegularSwiftSource(entry fs.DirEntry) bool {
	return strings.EqualFold(filepath.Ext(entry.Name()), swiftSourceExtension) && isRegularNonSymlink(entry)
}

func isRegularNonSymlink(entry fs.DirEntry) bool {
	if entry.Type()&fs.ModeSymlink != 0 {
		return false
	}
	info, err := entry.Info()
	return err == nil && info.Mode().IsRegular()
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
		if confidence := carthageDetectionConfidence(entry); confidence > 0 {
			carthageRoots[filepath.Dir(path)] += confidence
		}
		if isRegularSwiftSource(entry) {
			recordSwiftSourceDirectories(repoPath, path, swiftDirectories)
		}
		return recordSwiftDetectionEntry(path, entry, detection, roots)
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
	return shared.IsPureSentinelError(err, safeio.ErrTargetPathSymlink, fs.ErrNotExist)
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
	if err != nil || found {
		return found, rootEntries, err
	}

	remaining := maxEntries - rootEntries
	for index, directory := range directories {
		if remaining == 0 {
			break
		}
		if err := contextError(ctx); err != nil {
			return false, maxEntries - remaining, err
		}
		budget := max(1, remaining/(len(directories)-index))
		found, entries, err := findSwiftSourceWithinRootDirectory(ctx, root, directory, budget)
		if err != nil || found {
			return found, maxEntries - remaining + entries, err
		}
		remaining -= entries
	}
	return false, maxEntries - remaining, nil
}

func applyCarthageDetectionRoot(root string, confidence int, detection *language.Detection, roots map[string]struct{}) {
	detection.Matched = true
	detection.Confidence += confidence
	roots[root] = struct{}{}
}

func carthageDetectionConfidence(entry fs.DirEntry) int {
	switch strings.ToLower(entry.Name()) {
	case strings.ToLower(carthageManifestName), strings.ToLower(carthageResolvedName):
	default:
		return 0
	}
	if !isRegularNonSymlink(entry) {
		return 0
	}
	return 10
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
	return recordSwiftDetectionEntry(path, entry, detection, roots)
}

func recordSwiftDetectionEntry(path string, entry fs.DirEntry, detection *language.Detection, roots map[string]struct{}) error {
	switch strings.ToLower(entry.Name()) {
	case strings.ToLower(packageManifestName), strings.ToLower(packageResolvedName), strings.ToLower(podManifestName), strings.ToLower(podLockName):
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	}
	if isRegularSwiftSource(entry) {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
}
