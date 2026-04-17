package ruby

import (
	"context"
	"errors"
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
		{Name: gemfileName, Confidence: 60},
		{Name: gemfileLockName, Confidence: 30},
	}

	if err := shared.ApplyRootSignals(repoPath, rootSignals, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := walkRubyRepoFiles(ctx, repoPath, func(path string, entry fs.DirEntry) error {
		visited++
		if visited > maxDetectFiles {
			return fs.SkipAll
		}
		switch strings.ToLower(entry.Name()) {
		case strings.ToLower(gemfileName), strings.ToLower(gemfileLockName):
			detection.Matched = true
			detection.Confidence += 10
			roots[filepath.Dir(path)] = struct{}{}
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), gemspecExt) {
			detection.Matched = true
			detection.Confidence += 10
			roots[filepath.Dir(path)] = struct{}{}
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".rb") {
			detection.Matched = true
			detection.Confidence += 2
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}
