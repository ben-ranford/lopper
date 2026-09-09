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
	manifests := newManifestRootDiscovery()
	if err := applyRootSignals(repoPath, &detection, roots, manifests); err != nil {
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
		return updateDetection(repoPath, path, entry.Name(), &detection, roots, manifests)
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return language.Detection{}, err
	}
	manifests.isolateFallbackRoots(roots)

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}, manifestStates ...*manifestRootDiscovery) error {
	// .NET root detection is extension/pattern based and solution files can add
	// extra roots, so it cannot be represented as exact shared.RootSignal names.
	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(repoPath, name)
		if err := recordManifestState(repoPath, path, name, manifestStates...); err != nil {
			return err
		}
		if err := applyDetectionSignal(repoPath, path, name, repoPath, detection, roots, rootDetectionWeights); err != nil {
			return err
		}
	}
	return nil
}

type manifestRootDiscovery struct {
	validProjects     map[string]struct{}
	centralRoots      map[string]struct{}
	malformedProjects map[string]struct{}
	malformedCentral  map[string]struct{}
}

func newManifestRootDiscovery() *manifestRootDiscovery {
	return &manifestRootDiscovery{
		validProjects:     make(map[string]struct{}),
		centralRoots:      make(map[string]struct{}),
		malformedProjects: make(map[string]struct{}),
		malformedCentral:  make(map[string]struct{}),
	}
}

func updateDetection(repoPath, path, name string, detection *language.Detection, roots map[string]struct{}, manifestStates ...*manifestRootDiscovery) error {
	if err := recordManifestState(repoPath, path, name, manifestStates...); err != nil {
		return err
	}
	return applyDetectionSignal(repoPath, path, name, filepath.Dir(path), detection, roots, walkDetectionWeights)
}

func recordManifestState(repoPath, path, name string, manifestStates ...*manifestRootDiscovery) error {
	var manifests *manifestRootDiscovery
	if len(manifestStates) > 0 {
		manifests = manifestStates[0]
	}
	signal := signalForName(name)
	if signal == fileSignalProject || signal == fileSignalCentral {
		_, err := parseManifestDependenciesForEntry(repoPath, path, name)
		if err != nil && !isDotNetManifestParseError(err) {
			return err
		}
		if manifests != nil {
			manifests.record(filepath.Dir(path), signal, err != nil)
		}
	}
	return nil
}

func (m *manifestRootDiscovery) record(root string, signal fileSignal, malformed bool) {
	switch {
	case signal == fileSignalCentral:
		m.centralRoots[root] = struct{}{}
		if malformed {
			m.malformedCentral[root] = struct{}{}
		}
	case malformed:
		m.malformedProjects[root] = struct{}{}
	default:
		m.validProjects[root] = struct{}{}
	}
}

func (m *manifestRootDiscovery) isolateFallbackRoots(roots map[string]struct{}) {
	for centralRoot := range m.centralRoots {
		if !m.hasProjectAncestor(centralRoot) {
			continue
		}
		delete(m.malformedCentral, centralRoot)
		_, hasProject := m.validProjects[centralRoot]
		_, hasMalformedProject := m.malformedProjects[centralRoot]
		if !hasProject && !hasMalformedProject {
			delete(roots, centralRoot)
		}
	}
	for root := range m.malformedCentral {
		m.malformedProjects[root] = struct{}{}
	}
	isolateMalformedManifestRoots(roots, m.malformedProjects)
}

func (m *manifestRootDiscovery) hasProjectAncestor(root string) bool {
	for projectRoot := range m.validProjects {
		if isDotNetSubPath(projectRoot, root) {
			return true
		}
	}
	return false
}

func isolateMalformedManifestRoots(roots, malformedRoots map[string]struct{}) {
	for malformedRoot := range malformedRoots {
		owner := malformedRootOwner(malformedRoot, malformedRoots)
		for root := range roots {
			if isDotNetSubPath(owner, root) {
				delete(roots, root)
			}
		}
		roots[owner] = struct{}{}
	}
}

func malformedRootOwner(root string, malformedRoots map[string]struct{}) string {
	owner := root
	for candidate := range malformedRoots {
		if isDotNetSubPath(candidate, root) && len(candidate) < len(owner) {
			owner = candidate
		}
	}
	return owner
}

func isDotNetSubPath(parent, child string) bool {
	relativePath, err := filepath.Rel(parent, child)
	return err == nil && relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator))
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
