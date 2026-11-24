package rust

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	maxWorkspacePatterns     = 256
	maxWorkspacePatternBytes = 1024
	maxWorkspacePathParts    = 64
	maxWorkspaceEntries      = 32768
	maxWorkspaceCandidates   = 2048
	maxWorkspaceMatchCells   = 1 << 22
)

var (
	errWorkspacePatternLimit = errors.New("workspace pattern exceeds the size or depth limit")
	errWorkspacePathDepth    = errors.New("cargo workspace discovery reached the member path depth limit; results may be partial")
	errWorkspaceMatchBudget  = errors.New("cargo workspace discovery reached the pattern matching work limit; results may be partial")
)

type workspaceMemberPattern struct {
	text    string
	parts   []string
	matched bool
}

func discoverWorkspaceMemberManifests(repoPath string, members []string) ([]string, []string, []report.CoverageGap) {
	budget := shared.RootedWalkBudget{
		MaxTraversalEntries: maxWorkspaceEntries,
		MaxFiles:            maxWorkspaceCandidates,
		CountCandidate: func(_ string, entry fs.DirEntry) bool {
			return entry.Name() == cargoTomlName
		},
	}
	return discoverWorkspaceMemberManifestsWithBudget(repoPath, members, budget)
}

func discoverWorkspaceMemberManifestsWithBudget(repoPath string, members []string, budget shared.RootedWalkBudget) ([]string, []string, []report.CoverageGap) {
	var warnings []string
	var gaps []report.CoverageGap
	if len(members) > maxWorkspacePatterns {
		warning := fmt.Sprintf("cargo workspace discovery capped at %d member patterns", maxWorkspacePatterns)
		warnings = append(warnings, warning)
		gaps = append(gaps, workspaceMemberCoverageGap(warning))
		members = members[:maxWorkspacePatterns]
	}

	patterns := make([]workspaceMemberPattern, 0, len(members))
	for _, member := range members {
		parts, err := workspacePatternParts(member)
		switch {
		case errors.Is(err, errWorkspacePatternLimit):
			warnings = append(warnings, errWorkspacePatternLimit.Error())
			gaps = append(gaps, workspaceMemberCoverageGap(errWorkspacePatternLimit.Error()))
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("invalid workspace member pattern %q: %v", member, err))
		case len(parts) != 0:
			patterns = append(patterns, workspaceMemberPattern{text: member, parts: parts})
		}
	}
	if len(patterns) == 0 {
		return nil, warnings, gaps
	}

	paths, err := walkWorkspaceMemberManifests(repoPath, patterns, budget)
	if err != nil {
		warning, limited := shared.RootedWalkBudgetWarning("cargo workspace discovery", budget, err)
		if !limited {
			warning = fmt.Sprintf("cargo workspace discovery stopped: %v", err)
		}
		warnings = append(warnings, warning)
		gaps = append(gaps, workspaceMemberCoverageGap(warning))
		return paths, warnings, gaps
	}
	for _, pattern := range patterns {
		if !pattern.matched {
			warnings = append(warnings, fmt.Sprintf("workspace member pattern %q did not resolve to a Cargo.toml", pattern.text))
		}
	}
	return paths, warnings, gaps
}

func workspaceMemberCoverageGap(warning string) report.CoverageGap {
	return report.CoverageGap{
		Code:     report.CoverageGapRustManifestDiscoveryCap,
		Language: rustAdapterID,
		Path:     ".",
		Evidence: []string{warning},
	}
}

func walkWorkspaceMemberManifests(repoPath string, patterns []workspaceMemberPattern, budget shared.RootedWalkBudget) (_ []string, err error) {
	root, err := safeio.OpenRoot(repoPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()
	var paths []string
	remaining := maxWorkspaceMatchCells
	err = shared.WalkRepoFilesWithinRoot(context.Background(), repoPath, root, budget, shouldSkipDir, func(path string, entry fs.DirEntry) error {
		if entry.Name() != cargoTomlName {
			return nil
		}
		rel, err := filepath.Rel(repoPath, filepath.Dir(path))
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) > maxWorkspacePathParts {
			return errWorkspacePathDepth
		}
		matched, err := matchAnyWorkspaceMember(patterns, parts, &remaining)
		if err != nil {
			return err
		}
		if matched {
			paths = append(paths, path)
		}
		return nil
	})
	return paths, err
}

func matchAnyWorkspaceMember(patterns []workspaceMemberPattern, parts []string, remaining *int) (bool, error) {
	matched := false
	for i := range patterns {
		cells := len(patterns[i].parts) * (1 + len(parts))
		if cells > *remaining {
			return false, errWorkspaceMatchBudget
		}
		*remaining -= cells
		if matchWorkspaceMemberPatternParts(patterns[i].parts, parts) {
			patterns[i].matched = true
			matched = true
		}
	}
	return matched, nil
}

func workspacePatternParts(pattern string) ([]string, error) {
	if len(pattern) > maxWorkspacePatternBytes {
		return nil, errWorkspacePatternLimit
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, nil
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(pattern)), "/")
	if len(parts) > maxWorkspacePathParts {
		return nil, errWorkspacePatternLimit
	}
	for _, part := range parts {
		if _, err := filepath.Match(part, ""); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

// Each cell records whether the remaining pattern can match the remaining path.
// A recursive wildcard either consumes a segment or advances the pattern.
func matchWorkspaceMemberPatternParts(pattern, path []string) bool {
	var matches [maxWorkspacePathParts + 1]bool
	matches[len(path)] = true
	for i := len(pattern) - 1; i >= 0; i-- {
		next := matches[len(path)]
		matches[len(path)] = next && pattern[i] == "**"
		for j := len(path) - 1; j >= 0; j-- {
			previous := matches[j]
			if pattern[i] == "**" {
				matches[j] = previous || matches[j+1]
			} else {
				matches[j] = false
				if next {
					matched, err := filepath.Match(pattern[i], path[j])
					matches[j] = err == nil && matched
				}
			}
			next = previous
		}
	}
	return matches[0]
}
