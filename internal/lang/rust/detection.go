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
	workspaceOnlyRoot, err := applyRustRootSignals(repoPath, &detection, roots)
	if err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err = shared.WalkRepoFiles(ctx, repoPath, maxDetectionEntries, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		return walkRustDetectionEntry(path, entry, repoPath, workspaceOnlyRoot, roots, &detection, &visited)
	})
	if err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyRustRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) (bool, error) {
	workspaceOnlyRoot := false
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
		if len(meta.WorkspaceMembers) > 0 {
			workspaceOnlyRoot = !meta.HasPackage
			for _, member := range meta.WorkspaceMembers {
				addWorkspaceMemberRoot(repoPath, member, roots)
			}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}

	cargoLockPath := filepath.Join(repoPath, cargoLockName)
	if _, err := os.Stat(cargoLockPath); err == nil {
		detection.Matched = true
		detection.Confidence += 20
		if !workspaceOnlyRoot {
			roots[repoPath] = struct{}{}
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}

	return workspaceOnlyRoot, nil
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

func walkRustDetectionEntry(path string, entry fs.DirEntry, repoPath string, workspaceOnlyRoot bool, roots map[string]struct{}, detection *language.Detection, visited *int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}

	(*visited)++
	if *visited > maxDetectionEntries {
		return fs.SkipAll
	}

	name := strings.ToLower(entry.Name())
	switch name {
	case strings.ToLower(cargoTomlName):
		detection.Matched = true
		detection.Confidence += 12
		dir := filepath.Dir(path)
		if workspaceOnlyRoot && samePath(dir, repoPath) {
			return nil
		}
		roots[dir] = struct{}{}
	case strings.ToLower(cargoLockName):
		detection.Matched = true
		detection.Confidence += 6
	}

	if strings.EqualFold(filepath.Ext(path), ".rs") {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
}
