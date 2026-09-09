//go:build !regressionproof

package analysis

import (
	"path/filepath"
	"testing"
)

func TestAnalysisCacheIsolationRootsUseRelativeIdentity(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()
	cacheFor := func(repo string) *analysisCache {
		return &analysisCache{
			options:          resolvedCacheOptions{Enabled: true},
			cacheable:        true,
			stableRepoPath:   filepath.Join("stable", "repo"),
			analysisRepoPath: repo,
		}
	}

	first, err := cacheFor(repoA).prepareEntryWithIsolationRoots(Request{}, "dotnet", repoA, []string{filepath.Join(repoA, "nested")})
	if err != nil {
		t.Fatalf("prepare first isolation entry: %v", err)
	}
	second, err := cacheFor(repoB).prepareEntryWithIsolationRoots(Request{}, "dotnet", repoB, []string{filepath.Join(repoB, "nested")})
	if err != nil {
		t.Fatalf("prepare second isolation entry: %v", err)
	}
	if first.KeyDigest != second.KeyDigest {
		t.Fatalf("expected equivalent relative isolation roots to share a cache key: %q != %q", first.KeyDigest, second.KeyDigest)
	}
	different, err := cacheFor(repoB).prepareEntryWithIsolationRoots(Request{}, "dotnet", repoB, []string{filepath.Join(repoB, "other")})
	if err != nil {
		t.Fatalf("prepare different isolation entry: %v", err)
	}
	if first.KeyDigest == different.KeyDigest {
		t.Fatalf("expected different relative isolation roots to produce distinct cache keys")
	}
}
