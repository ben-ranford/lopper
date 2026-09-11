package swift

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
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

	if err := walkSwiftDetection(ctx, repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func walkSwiftDetection(ctx context.Context, repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	carthageRoots := make(map[string]int)
	swiftDirectories := make(map[string]struct{})
	err := shared.WalkRepoFiles(ctx, repoPath, maxDetectFiles, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		if confidence := carthageDetectionConfidence(repoPath, path, entry.Name()); confidence > 0 {
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

func carthageDetectionConfidence(repoPath, path, name string) int {
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
