package rust

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestRustWorkspaceDiscoveryReportsLimits(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "member", cargoTomlName), "[package]\nname = \"member\"")
	_, warnings, gaps := discoverWorkspaceMemberManifestsWithBudget(repo, []string{"member"}, shared.RootedWalkBudget{MaxTraversalEntries: 1})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "rooted walk limit") {
		t.Fatalf("expected one traversal limit warning, got %v", warnings)
	}
	if len(gaps) != 1 || gaps[0].Code != report.CoverageGapRustManifestDiscoveryCap {
		t.Fatalf("expected one manifest discovery coverage gap, got %v", gaps)
	}

	_, err := workspacePatternParts(strings.Repeat("a", maxWorkspacePatternBytes+1))
	if !errors.Is(err, errWorkspacePatternLimit) {
		t.Fatalf("expected a pattern size limit, got %v", err)
	}
	pattern := []string{"workspace", "*-member"}
	path := []string{"workspace", "first-member"}
	patterns := []workspaceMemberPattern{{parts: pattern, width: workspaceMatchWidth(pattern)}}
	work := workspaceMatchWidth(pattern) * workspaceMatchWidth(path)
	remaining := work - 1
	if _, err := matchAnyWorkspaceMember(patterns, path, &remaining); !errors.Is(err, errWorkspaceMatchBudget) {
		t.Fatalf("expected a pattern matching work limit, got %v", err)
	}
	remaining = work
	if matched, err := matchAnyWorkspaceMember(patterns, path, &remaining); err != nil || !matched || remaining != 0 {
		t.Fatalf("expected one match at the exact work limit, got matched=%t remaining=%d err=%v", matched, remaining, err)
	}
}

func TestRustWorkspaceDiscoveryReportsSkippedPatterns(t *testing.T) {
	repo := t.TempDir()
	members := slices.Repeat([]string{"ordinary-member"}, maxWorkspacePatterns+1)
	members[0] = strings.Repeat("x", maxWorkspacePatternBytes+1)
	members[1] = strings.Repeat("a/", maxWorkspacePathParts) + "member"
	_, warnings, gaps := discoverWorkspaceMemberManifests(repo, members)
	if !strings.Contains(strings.Join(warnings, "\n"), "member patterns") || len(gaps) != 3 {
		t.Fatalf("expected one count and two pattern limits, got warnings %v and gaps %v", warnings, gaps)
	}

	missingRepo := filepath.Join(repo, "missing")
	_, warnings, gaps = discoverWorkspaceMemberManifests(missingRepo, []string{"ordinary-member"})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "discovery stopped") || len(gaps) != 1 {
		t.Fatalf("expected unavailable repository coverage warning, got %v and %v", warnings, gaps)
	}
}

func TestRustWorkspaceDiscoveryReportsPathDepthLimit(t *testing.T) {
	repo := t.TempDir()
	member := filepath.Join(repo, filepath.FromSlash(strings.Repeat("a/", maxWorkspacePathParts)+"member"))
	writeFile(t, filepath.Join(member, cargoTomlName), "[package]\nname = 'member'")
	_, warnings, gaps := discoverWorkspaceMemberManifests(repo, []string{"**"})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "member path depth limit") || len(gaps) != 1 {
		t.Fatalf("expected member path depth warning, got %v and %v", warnings, gaps)
	}
}

func workspaceMemberPatternMatches(pattern, candidate string) (bool, error) {
	patternParts, err := workspacePatternParts(pattern)
	if err != nil || len(patternParts) == 0 || strings.TrimSpace(candidate) == "" {
		return false, err
	}
	return matchWorkspaceMemberPatternParts(patternParts, strings.Split(filepath.ToSlash(filepath.Clean(candidate)), "/")), nil
}

func resolveWorkspaceMemberManifestPaths(repoPath, member string) ([]string, string) {
	paths, warnings, _ := discoverWorkspaceMemberManifests(repoPath, []string{member})
	return paths, strings.Join(warnings, "\n")
}

func resolveWorkspaceMembers(repoPath, pattern string) []string {
	paths, _ := resolveWorkspaceMemberManifestPaths(repoPath, pattern)
	roots := make([]string, 0, len(paths))
	for _, path := range paths {
		roots = append(roots, filepath.Dir(path))
	}
	return roots
}
