package dotnet

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

type detectionWeights struct {
	central  int
	project  int
	solution int
	source   int
}

var (
	rootDetectionWeights = detectionWeights{central: 45, project: 55, solution: 50}
	walkDetectionWeights = detectionWeights{central: 10, project: 12, solution: 8, source: 2}
)

type fileSignal int

const (
	fileSignalNone fileSignal = iota
	fileSignalCentral
	fileSignalProject
	fileSignalSolution
	fileSignalSource
)

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})
	if err := applyRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > maxDetectFiles {
			return fs.SkipAll
		}
		return updateDetection(repoPath, path, entry.Name(), &detection, roots)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
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
		name := entry.Name()
		path := filepath.Join(repoPath, name)
		if err := applyDetectionSignal(repoPath, path, name, repoPath, detection, roots, rootDetectionWeights); err != nil {
			return err
		}
	}
	return nil
}

func updateDetection(repoPath, path, name string, detection *language.Detection, roots map[string]struct{}) error {
	return applyDetectionSignal(repoPath, path, name, filepath.Dir(path), detection, roots, walkDetectionWeights)
}

func applyDetectionSignal(repoPath, path, name, root string, detection *language.Detection, roots map[string]struct{}, weights detectionWeights) error {
	signal := signalForName(name)
	switch signal {
	case fileSignalCentral:
		markDetection(detection, roots, weights.central, root)
	case fileSignalProject:
		markDetection(detection, roots, weights.project, root)
	case fileSignalSolution:
		markDetection(detection, roots, weights.solution, root)
		if err := addSolutionRoots(repoPath, path, roots); err != nil {
			return err
		}
	case fileSignalSource:
		markDetection(detection, roots, weights.source, "")
	}
	return nil
}

func signalForName(name string) fileSignal {
	lower := strings.ToLower(name)
	switch {
	case strings.EqualFold(name, centralPackagesFile):
		return fileSignalCentral
	case isProjectManifestName(lower):
		return fileSignalProject
	case isSolutionFileName(lower):
		return fileSignalSolution
	case isSourceFileName(lower):
		return fileSignalSource
	default:
		return fileSignalNone
	}
}

func markDetection(detection *language.Detection, roots map[string]struct{}, confidence int, root string) {
	if confidence <= 0 {
		return
	}
	detection.Matched = true
	detection.Confidence += confidence
	if root != "" {
		roots[root] = struct{}{}
	}
}
