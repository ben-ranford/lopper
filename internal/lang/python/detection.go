package python

import (
	"context"
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

	if err := applyPythonRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	err := shared.WalkRepoFiles(ctx, repoPath, 512, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		updateDetectionFromPythonFile(path, entry, roots, &detection)
		return nil
	})
	if err != nil {
		return language.Detection{}, err
	}

	detection = shared.FinalizeDetection(repoPath, detection, roots)
	return detection, nil
}

func applyPythonRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	return shared.ApplyRootSignals(repoPath, pythonRootSignals, detection, roots)
}

var pythonRootSignals = []shared.RootSignal{
	{Name: "pyproject.toml", Confidence: 50},
	{Name: "Pipfile", Confidence: 45},
	{Name: "Pipfile.lock", Confidence: 25},
	{Name: "poetry.lock", Confidence: 25},
	{Name: "uv.lock", Confidence: 25},
	{Name: "requirements.txt", Confidence: 35},
	{Name: "setup.py", Confidence: 35},
}

func updateDetectionFromPythonFile(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection) {
	switch strings.ToLower(entry.Name()) {
	case "pyproject.toml", "pipfile", "pipfile.lock", "poetry.lock", "uv.lock", "requirements.txt", "setup.py":
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	}
	if strings.HasSuffix(strings.ToLower(path), ".py") {
		detection.Matched = true
		detection.Confidence += 2
	}
}
