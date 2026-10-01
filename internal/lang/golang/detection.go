package golang

import (
	"context"
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

	err := shared.WalkRepoFiles(ctx, repoPath, 1024, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		updateGoDetection(path, entry, roots, &detection)
		return nil
	})
	if err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
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
	if err := shared.ApplyRootSignals(repoPath, goRootSignals, detection, roots); err != nil {
		return err
	}
	goWorkExists, err := manifestPathExists(filepath.Join(repoPath, goWorkName))
	if err != nil {
		return err
	}
	if goWorkExists {
		return addGoWorkRoots(repoPath, roots)
	}
	return nil
}

var goRootSignals = []shared.RootSignal{
	{Name: goModName, Confidence: 55},
	{Name: goWorkName, Confidence: 45},
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
