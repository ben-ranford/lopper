package dart

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
	if err := applyDartRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return walkDartDetectionEntry(path, entry, roots, &detection, &visited)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyDartRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	return shared.ApplyRootSignals(repoPath, dartRootSignals, detection, roots)
}

func walkDartDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}

	(*visited)++
	if *visited > maxDetectionEntries {
		return fs.SkipAll
	}

	name := strings.ToLower(entry.Name())
	switch name {
	case pubspecYAMLName, pubspecYMLName:
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
	case pubspecLockName:
		detection.Matched = true
		detection.Confidence += 6
	}

	if strings.EqualFold(filepath.Ext(path), ".dart") {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
}
