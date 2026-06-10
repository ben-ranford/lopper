package php

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
)

const (
	composerJSONName = "composer.json"
	composerLockName = "composer.lock"
	maxDetectFiles   = 1024
	maxScanFiles     = 2048
)

var phpRootSignals = []shared.RootSignal{
	{Name: composerJSONName, Confidence: 60},
	{Name: composerLockName, Confidence: 30},
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := applyPHPRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := shared.WalkRepoFiles(ctx, repoPath, 0, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		return walkPHPDetectionEntry(path, entry, roots, &detection, &visited, maxDetectFiles)
	})
	if err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyPHPRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	return shared.ApplyRootSignals(repoPath, phpRootSignals, detection, roots)
}

func walkPHPDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}

	*visited++
	if *visited > maxFiles {
		return fs.SkipAll
	}

	switch strings.ToLower(entry.Name()) {
	case composerJSONName, composerLockName:
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
	}

	if strings.EqualFold(filepath.Ext(path), ".php") {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
}
