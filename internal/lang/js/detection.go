package js

import (
	"context"
	"errors"
	"io"
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
	_ = ctx
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := addRootSignalDetection(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	err := scanFilesForJSDetection(repoPath, &detection, roots)
	if errors.Is(err, io.EOF) {
		err = nil
	}
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

func scanFilesForJSDetection(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	const maxFiles = 256
	visitedFiles := 0
	return filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if shouldSkipDetectDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		visitedFiles++
		if visitedFiles > maxFiles {
			return io.EOF
		}
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
