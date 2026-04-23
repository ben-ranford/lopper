package powershell

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

	if err := applyRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	err := shared.WalkRepoFiles(ctx, repoPath, maxDetectFiles, shouldSkipPowerShellDir, func(path string, entry fs.DirEntry) error {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		switch ext {
		case moduleManifestExt:
			detection.Matched = true
			detection.Confidence += 12
			roots[filepath.Dir(path)] = struct{}{}
		case moduleScriptExt:
			detection.Matched = true
			detection.Confidence += 4
		case scriptExt:
			detection.Matched = true
			detection.Confidence += 2
		}
		return nil
	})
	if err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		switch ext {
		case moduleManifestExt:
			detection.Matched = true
			detection.Confidence += 65
			roots[repoPath] = struct{}{}
		case moduleScriptExt:
			detection.Matched = true
			detection.Confidence += 15
		case scriptExt:
			detection.Matched = true
			detection.Confidence += 3
		}
	}
	return nil
}

func shouldSkipPowerShellDir(name string) bool {
	return shared.ShouldSkipDir(strings.ToLower(name), powerShellSkippedDirs)
}
