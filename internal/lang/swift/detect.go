package swift

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
)

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection := language.Detection{}
	roots := make(map[string]struct{})
	rootSignals := []shared.RootSignal{
		{Name: packageManifestName, Confidence: 60},
		{Name: packageResolvedName, Confidence: 25},
	}
	if err := shared.ApplyRootSignals(repoPath, rootSignals, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	err := walkSwiftDetection(ctx, repoPath, &detection, roots)
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func walkSwiftDetection(ctx context.Context, repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	visited := 0
	return filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return detectSwiftEntry(ctx, path, entry, detection, roots, &visited)
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
	recordSwiftDetection(path, entry.Name(), detection, roots)
	return nil
}

func recordSwiftDetection(path string, name string, detection *language.Detection, roots map[string]struct{}) {
	switch strings.ToLower(name) {
	case strings.ToLower(packageManifestName), strings.ToLower(packageResolvedName):
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	}
	if strings.EqualFold(filepath.Ext(name), ".swift") {
		detection.Matched = true
		detection.Confidence += 2
	}
}
