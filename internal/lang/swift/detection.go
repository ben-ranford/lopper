package swift

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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

	rootCarthageCorroborated, err := applyRootCarthageSignals(ctx, repoPath, &detection, roots)
	if err != nil {
		return language.Detection{}, err
	}

	if err := walkSwiftDetection(ctx, repoPath, &detection, roots, rootCarthageCorroborated); err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyRootCarthageSignals(ctx context.Context, repoPath string, detection *language.Detection, roots map[string]struct{}) (bool, error) {
	confidence, err := rootCarthageDetectionConfidence(repoPath)
	if err != nil || confidence == 0 {
		return false, err
	}
	corroborated, err := hasSwiftSourceNearRoot(ctx, repoPath)
	if err != nil || !corroborated {
		return false, err
	}
	detection.Matched = true
	detection.Confidence += confidence
	roots[repoPath] = struct{}{}
	return true, nil
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

func hasSwiftSourceNearRoot(ctx context.Context, repoPath string) (found bool, err error) {
	if err := contextError(ctx); err != nil {
		return false, err
	}
	root, err := safeio.OpenRootNoFollow(repoPath)
	if err != nil {
		return false, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	directories, rootEntries, found, err := discoverRootSwiftSourceCandidates(ctx, root)
	if err != nil || found {
		return found, err
	}

	remaining := maxRootCarthageSourceTraversalEntries - rootEntries
	for index, directory := range directories {
		if remaining == 0 {
			break
		}
		if err := contextError(ctx); err != nil {
			return false, err
		}
		budget := max(1, remaining/(len(directories)-index))
		found, entries, err := findSwiftSourceWithinRootDirectory(ctx, root, directory, budget)
		if err != nil || found {
			return found, err
		}
		remaining -= entries
	}
	return false, nil
}

func discoverRootSwiftSourceCandidates(ctx context.Context, root safeio.Root) (directories []string, entriesSeen int, found bool, err error) {
	directory, err := safeio.OpenPinnedDirectory(root, ".")
	if err != nil {
		return nil, 0, false, err
	}
	defer func() {
		err = errors.Join(err, directory.Close())
	}()

	for entriesSeen < maxRootCarthageSourceRootEntries {
		if err := contextError(ctx); err != nil {
			return nil, entriesSeen, false, err
		}
		entries, readErr := directory.ReadDir(min(rootCarthageSourceReadBatchSize, maxRootCarthageSourceRootEntries-entriesSeen))
		entriesSeen += len(entries)
		for _, entry := range entries {
			if isRegularSwiftSource(entry) {
				return nil, entriesSeen, true, nil
			}
			if entry.IsDir() && entry.Type()&fs.ModeSymlink == 0 && !shouldSkipDir(entry.Name()) {
				directories = append(directories, entry.Name())
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, entriesSeen, false, readErr
		}
	}
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
		directory, openErr := safeio.OpenPinnedDirectory(root, candidate.path)
		if openErr != nil {
			return false, entriesSeen, openErr
		}

		for entriesSeen < maxEntries {
			if err := contextError(ctx); err != nil {
				return false, entriesSeen, errors.Join(err, directory.Close())
			}
			entries, readErr := directory.ReadDir(min(rootCarthageSourceReadBatchSize, maxEntries-entriesSeen))
			entriesSeen += len(entries)
			for _, entry := range entries {
				if isRegularSwiftSource(entry) {
					return true, entriesSeen, directory.Close()
				}
				if entry.IsDir() && entry.Type()&fs.ModeSymlink == 0 && !shouldSkipDir(entry.Name()) && candidate.depth < maxRootCarthageSourceDepth {
					queue = append(queue, rootCarthageSourceDirectory{path: filepath.Join(candidate.path, entry.Name()), depth: candidate.depth + 1})
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return false, entriesSeen, errors.Join(readErr, directory.Close())
			}
		}
		if closeErr := directory.Close(); closeErr != nil {
			return false, entriesSeen, closeErr
		}
	}
	return false, entriesSeen, nil
}

type rootCarthageSourceDirectory struct {
	path  string
	depth int
}

func isRegularSwiftSource(entry fs.DirEntry) bool {
	if !strings.EqualFold(filepath.Ext(entry.Name()), ".swift") || entry.Type()&fs.ModeSymlink != 0 {
		return false
	}
	info, err := entry.Info()
	return err == nil && info.Mode().IsRegular()
}

func walkSwiftDetection(ctx context.Context, repoPath string, detection *language.Detection, roots map[string]struct{}, rootCarthageCorroborated bool) error {
	carthageRoots := make(map[string]int)
	swiftDirectories := make(map[string]struct{})
	if rootCarthageCorroborated {
		swiftDirectories[filepath.Clean(repoPath)] = struct{}{}
	}
	err := shared.WalkRepoFiles(ctx, repoPath, maxDetectFiles, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		if confidence := carthageDetectionConfidence(repoPath, path, entry.Name(), rootCarthageCorroborated); confidence > 0 {
			carthageRoots[filepath.Dir(path)] += confidence
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".swift") {
			recordSwiftSourceDirectories(repoPath, path, swiftDirectories)
		}
		return recordSwiftDetectionEntry(path, entry.Name(), detection, roots)
	})
	if err != nil {
		return err
	}
	for root, confidence := range carthageRoots {
		if _, corroborated := swiftDirectories[root]; corroborated {
			roots[root] = struct{}{}
			detection.Confidence += confidence
		}
	}
	return nil
}

func carthageDetectionConfidence(repoPath, path, name string, rootCarthageCorroborated bool) int {
	rootConfidence := 0
	switch strings.ToLower(name) {
	case strings.ToLower(carthageManifestName):
		rootConfidence = 60
	case strings.ToLower(carthageResolvedName):
		rootConfidence = 25
	default:
		return 0
	}
	if filepath.Dir(path) == filepath.Clean(repoPath) {
		if rootCarthageCorroborated {
			return 10
		}
		return 10 + rootConfidence
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
	return recordSwiftDetectionEntry(path, entry.Name(), detection, roots)
}

func recordSwiftDetectionEntry(path string, name string, detection *language.Detection, roots map[string]struct{}) error {
	switch strings.ToLower(name) {
	case strings.ToLower(packageManifestName), strings.ToLower(packageResolvedName), strings.ToLower(podManifestName), strings.ToLower(podLockName):
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	}
	if strings.EqualFold(filepath.Ext(name), ".swift") {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
}
