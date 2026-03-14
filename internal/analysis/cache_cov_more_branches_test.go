package analysis

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestAnalysisCacheAdditionalBranchCoverage(t *testing.T) {
	repo := t.TempDir()
	root := mustCreateRootWithGoMod(t, repo, "pkg")
	cache := &analysisCache{options: resolvedCacheOptions{Enabled: true, Path: filepath.Join(repo, cacheDirName)}, cacheable: true}
	req := Request{
		Dependency: "dep",
		RemovalCandidateWeights: &report.RemovalCandidateWeights{
			Usage: math.NaN(),
		},
	}
	if _, err := cache.prepareEntry(req, "js-ts", root); err == nil {
		t.Fatalf("expected prepareEntry to fail when key payload cannot be marshaled")
	}

	configDir := filepath.Join(repo, "config-dir")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if _, err := cache.computeInputDigest(root, configDir); err == nil {
		t.Fatalf("expected computeInputDigest to fail for unreadable config path")
	}

	mustMkdirCacheLayout(t, cache.options.Path)
	entry := cacheEntryDescriptor{KeyDigest: "nan", InputDigest: "input"}
	if err := cache.store(entry, report.Report{
		Dependencies: []report.DependencyReport{{
			Name: "dep",
			RemovalCandidate: &report.RemovalCandidate{
				Score: math.NaN(),
			},
		}},
	}); err == nil {
		t.Fatalf("expected cache store to fail for NaN report payload")
	}
}

func TestAnalysisCacheAdditionalAtomicWriteErrors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if err := writeFileAtomic(filepath.Join(blocker, "child.json"), []byte("x")); err == nil {
		t.Fatalf("expected atomic write to fail when parent path is a file")
	}

	if runtime.GOOS == "windows" {
		t.Skip("permission-based temp-file creation failures are not portable on windows")
	}

	readOnlyDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(readOnlyDir, 0o500); err != nil {
		t.Fatalf("mkdir readonly dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(readOnlyDir, 0o700); err != nil {
			t.Fatalf("restore readonly dir perms: %v", err)
		}
	})
	if err := writeFileAtomic(filepath.Join(readOnlyDir, "child.json"), []byte("x")); err == nil {
		t.Fatalf("expected atomic write to fail when temp file cannot be created")
	}
}
