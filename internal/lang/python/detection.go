package python

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
	_ = ctx
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := applyPythonRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	const maxFiles = 512
	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return walkPythonDetectionEntry(path, entry, roots, &detection, &visited, maxFiles)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return language.Detection{}, err
	}

	detection = shared.FinalizeDetection(repoPath, detection, roots)
	return detection, nil
}

func walkPythonDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int) error {
	if pythonDetectionSkipsDir(entry) {
		return filepath.SkipDir
	}
	if entry.IsDir() {
		return nil
	}
	if pythonDetectionExceedsLimit(visited, maxFiles) {
		return fs.SkipAll
	}
	updateDetectionFromPythonFile(path, entry, roots, detection)
	return nil
}

func pythonDetectionSkipsDir(entry fs.DirEntry) bool {
	return entry.IsDir() && shouldSkipDir(entry.Name())
}

func pythonDetectionExceedsLimit(visited *int, maxFiles int) bool {
	(*visited)++
	return *visited > maxFiles
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
