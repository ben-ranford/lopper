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
	return shared.WalkRepoFiles(ctx, repoPath, maxDetectFiles, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		return recordSwiftDetectionEntry(path, entry.Name(), detection, roots)
	})
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
