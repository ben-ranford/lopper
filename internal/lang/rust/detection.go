package rust

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
	malformedRoots := make(map[string]struct{})
	malformedRoot := false
	workspaceOnlyRoot, err := applyRustRootSignals(repoPath, &detection, roots)
	if err != nil {
		if isCargoManifestParseError(err) {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return language.Detection{}, ctxErr
			}
			// The malformed root cannot define package boundaries. Analyze one
			// repository fallback so root sources and children are covered once,
			// while retaining nested manifests, locks, and source signals.
			roots[repoPath] = struct{}{}
			malformedRoots[repoPath] = struct{}{}
			malformedRoot = true
			workspaceOnlyRoot = true
		} else {
			return language.Detection{}, err
		}
	}

	walker := rustDetectionWalker{
		repoPath:          repoPath,
		workspaceOnlyRoot: workspaceOnlyRoot,
		roots:             roots,
		malformedRoots:    malformedRoots,
		detection:         &detection,
	}
	err = shared.WalkRepoFiles(ctx, repoPath, maxDetectionEntries, shouldSkipDir, walker.walk)
	if err != nil {
		return language.Detection{}, err
	}
	if malformedRoot {
		clear(roots)
		roots[repoPath] = struct{}{}
	}
	isolateMalformedRustRoots(roots, malformedRoots)

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyRustRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) (bool, error) {
	workspaceOnlyRoot, err := applyRootCargoManifestSignal(repoPath, detection, roots)
	if err != nil {
		return false, err
	}
	if err := applyRootCargoLockSignal(repoPath, workspaceOnlyRoot, detection, roots); err != nil {
		return false, err
	}
	return workspaceOnlyRoot, nil
}

func applyRootCargoManifestSignal(repoPath string, detection *language.Detection, roots map[string]struct{}) (bool, error) {
	cargoTomlPath := filepath.Join(repoPath, cargoTomlName)
	if _, err := os.Stat(cargoTomlPath); err == nil {
		detection.Matched = true
		detection.Confidence += 60

		meta, _, parseErr := parseCargoManifest(cargoTomlPath, repoPath)
		if parseErr != nil {
			return false, parseErr
		}
		if meta.HasPackage {
			roots[repoPath] = struct{}{}
		}
		if len(meta.WorkspaceMembers) == 0 {
			return false, nil
		}
		for _, member := range meta.WorkspaceMembers {
			addWorkspaceMemberRoot(repoPath, member, roots)
		}
		return !meta.HasPackage, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

func applyRootCargoLockSignal(repoPath string, workspaceOnlyRoot bool, detection *language.Detection, roots map[string]struct{}) error {
	cargoLockPath := filepath.Join(repoPath, cargoLockName)
	if _, err := os.Stat(cargoLockPath); err == nil {
		detection.Matched = true
		detection.Confidence += 20
		if !workspaceOnlyRoot {
			roots[repoPath] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func addWorkspaceMemberRoot(repoPath, member string, roots map[string]struct{}) {
	member = strings.TrimSpace(member)
	if member == "" {
		return
	}
	pattern := filepath.Join(repoPath, member)
	candidates, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		info, statErr := os.Stat(candidate)
		if statErr != nil || !info.IsDir() {
			continue
		}
		manifestPath := filepath.Join(candidate, cargoTomlName)
		if _, manifestErr := os.Stat(manifestPath); manifestErr != nil {
			continue
		}
		if !isSubPath(repoPath, candidate) {
			continue
		}
		roots[candidate] = struct{}{}
	}
}

type rustDetectionWalker struct {
	repoPath          string
	workspaceOnlyRoot bool
	roots             map[string]struct{}
	malformedRoots    map[string]struct{}
	detection         *language.Detection
	visited           int
}

func walkRustDetectionEntry(path string, entry fs.DirEntry, repoPath string, workspaceOnlyRoot bool, roots map[string]struct{}, detection *language.Detection, visited *int) error {
	walker := rustDetectionWalker{
		repoPath:          repoPath,
		workspaceOnlyRoot: workspaceOnlyRoot,
		roots:             roots,
		detection:         detection,
		visited:           *visited,
	}
	err := walker.walk(path, entry)
	*visited = walker.visited
	return err
}

func (w *rustDetectionWalker) walk(path string, entry fs.DirEntry) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}

	w.visited++
	if w.visited > maxDetectionEntries {
		return fs.SkipAll
	}

	name := strings.ToLower(entry.Name())
	switch name {
	case strings.ToLower(cargoTomlName):
		w.detection.Matched = true
		w.detection.Confidence += 12
		dir := filepath.Dir(path)
		if _, _, err := parseCargoManifest(path, w.repoPath); err != nil {
			if isCargoManifestParseError(err) {
				if w.malformedRoots != nil {
					w.malformedRoots[dir] = struct{}{}
				}
			} else {
				return err
			}
		}
		if w.workspaceOnlyRoot && samePath(dir, w.repoPath) {
			return nil
		}
		w.roots[dir] = struct{}{}
	case strings.ToLower(cargoLockName):
		w.detection.Matched = true
		w.detection.Confidence += 4
	}
	if filepath.Ext(name) == ".rs" {
		w.detection.Matched = true
		w.detection.Confidence += 2
	}
	return nil
}

func isolateMalformedRustRoots(roots, malformedRoots map[string]struct{}) {
	for root := range roots {
		for malformedRoot := range malformedRoots {
			if !samePath(root, malformedRoot) && isSubPath(malformedRoot, root) {
				delete(roots, root)
				break
			}
		}
	}
}
