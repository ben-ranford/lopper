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
const jsDetectionFileLimit = 256

var jsDetectSkippedDirs = map[string]bool{
	".next":    true,
	".turbo":   true,
	"coverage": true,
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	if err := ctx.Err(); err != nil {
		return language.Detection{}, err
	}
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := addRootSignalDetection(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}
	if err := ctx.Err(); err != nil {
		return language.Detection{}, err
	}
	if err := scanFilesForJSDetection(ctx, repoPath, &detection, roots); err != nil {
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
	scanner := jsDetectionScanner{detection: detection, roots: roots}
	summary, err := walkScanRepo(ctx, repoPath, defaultJSWalkEntryBudget, scanner.visit)
	return rootWalkResult(summary, err)
}

type jsDetectionScanner struct {
	detection   *language.Detection
	roots       map[string]struct{}
	visitedFile int
}

func (s *jsDetectionScanner) visit(path string, entry fs.DirEntry) error {
	if entry.IsDir() {
		if shouldSkipDetectDir(entry.Name()) {
			return fs.SkipDir
		}
		return nil
	}

	s.visitedFile++
	s.recordSignal(path, entry.Name())
	if s.visitedFile >= jsDetectionFileLimit {
		return fs.SkipAll
	}
	return nil
}

func (s *jsDetectionScanner) recordSignal(path, name string) {
	if strings.EqualFold(name, jsPackageFile) {
		s.detection.Matched = true
		s.detection.Confidence += 10
		s.roots[filepath.Dir(path)] = struct{}{}
		return
	}
	if isJSExtension(strings.ToLower(filepath.Ext(name))) {
		s.detection.Matched = true
		s.detection.Confidence += 2
	}
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
