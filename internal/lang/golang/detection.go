package golang

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
)

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})
	if err := applyGoRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	const maxFiles = 1024
	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return walkGoDetectionEntry(path, entry, roots, &detection, &visited, maxFiles)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func walkGoDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	(*visited)++
	if *visited > maxFiles {
		return fs.SkipAll
	}
	updateGoDetection(path, entry, roots, detection)
	return nil
}

func manifestPathExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() {
		return false, nil
	}
	return true, nil
}

func applyGoRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	rootSignals := []struct {
		name       string
		confidence int
	}{
		{name: goModName, confidence: 55},
		{name: goWorkName, confidence: 45},
	}
	for _, signal := range rootSignals {
		candidate := filepath.Join(repoPath, signal.name)
		exists, err := manifestPathExists(candidate)
		if err != nil {
			return err
		}
		if exists {
			detection.Matched = true
			detection.Confidence += signal.confidence
			roots[repoPath] = struct{}{}
			if signal.name == goWorkName {
				if err := addGoWorkRoots(repoPath, roots); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func addGoWorkRoots(repoPath string, roots map[string]struct{}) error {
	moduleDirs, err := goWorkModuleDirs(repoPath)
	if err != nil {
		return err
	}
	for dir := range moduleDirs {
		roots[dir] = struct{}{}
	}
	return nil
}

func updateGoDetection(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection) {
	switch strings.ToLower(entry.Name()) {
	case goModName, goWorkName:
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
	}
	if strings.EqualFold(filepath.Ext(path), ".go") {
		detection.Matched = true
		detection.Confidence += 2
	}
}
