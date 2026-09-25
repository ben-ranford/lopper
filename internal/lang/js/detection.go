package js

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
)

const jsPackageFile = "package.json"

var jsDetectSkippedDirs = map[string]bool{
	".next":    true,
	".turbo":   true,
	"coverage": true,
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := addRootSignalDetection(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	err := scanFilesForJSDetection(ctx, repoPath, &detection, roots)
	if err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func addRootSignalDetection(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	if err := shared.ApplyRootSignals(repoPath, jsPackageRootSignals, detection, roots); err != nil {
		return err
	}
	return shared.ApplyRootSignals(repoPath, jsConfigRootSignals, detection, nil)
}

var jsPackageRootSignals = []shared.RootSignal{
	{Name: jsPackageFile, Confidence: 45},
}

var jsConfigRootSignals = []shared.RootSignal{
	{Name: "tsconfig.json", Confidence: 20},
	{Name: "jsconfig.json", Confidence: 20},
}

func scanFilesForJSDetection(ctx context.Context, repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	const maxFiles = 256
	return shared.WalkRepoFiles(ctx, repoPath, maxFiles, shouldSkipDetectDir, func(path string, d fs.DirEntry) error {
		if strings.EqualFold(d.Name(), jsPackageFile) {
			detection.Matched = true
			detection.Confidence += 10
			roots[filepath.Dir(path)] = struct{}{}
			return nil
		}
		if isJSExtension(strings.ToLower(filepath.Ext(d.Name()))) {
			detection.Matched = true
			detection.Confidence += 2
		}
		return nil
	})
}

func shouldSkipDetectDir(name string) bool {
	return shared.ShouldSkipDir(name, jsDetectSkippedDirs)
}

func isJSExtension(ext string) bool {
	switch ext {
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts":
		return true
	default:
		return false
	}
}
