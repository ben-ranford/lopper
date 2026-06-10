package analysis

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

type cacheFailAfterWriter struct {
	failOn int
	writes int
}

type cacheStoreFailureCase struct {
	name       string
	blockedDir string
	keyDigest  string
}

func (w *cacheFailAfterWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failOn {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

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
	if cache.store(entry, report.Report{
		Dependencies: []report.DependencyReport{{
			Name: "dep",
			RemovalCandidate: &report.RemovalCandidate{
				Score: math.NaN(),
			},
		}},
	}) == nil {
		t.Fatalf("expected cache store to fail for NaN report payload")
	}
}

func TestAnalysisCacheAdditionalAtomicWriteErrors(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if writeFileAtomic(filepath.Join(blocker, "child.json"), []byte("x")) == nil {
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
	if writeFileAtomic(filepath.Join(readOnlyDir, "child.json"), []byte("x")) == nil {
		t.Fatalf("expected atomic write to fail when temp file cannot be created")
	}
}

func TestAnalysisCacheAdditionalWriteBranches(t *testing.T) {
	dir := t.TempDir()
	targetPath := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	t.Run("writeInputDigestRecord propagates writer failures", func(t *testing.T) {
		cases := []struct {
			name   string
			failOn int
		}{
			{name: "sort key", failOn: 1},
			{name: "separator", failOn: 2},
			{name: "digest", failOn: 3},
			{name: "newline", failOn: 4},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if err := writeInputDigestRecord(&cacheFailAfterWriter{failOn: tc.failOn}, cacheDigestInput{sortKey: "tracked", path: targetPath}); err == nil {
					t.Fatalf("expected writeInputDigestRecord to fail on write %d", tc.failOn)
				}
			})
		}
	})

	t.Run("buildRelevantFile rejects invalid root", func(t *testing.T) {
		if _, err := buildRelevantFile("\x00", targetPath); err == nil {
			t.Fatalf("expected buildRelevantFile to fail for invalid root path")
		}
	})

	t.Run("writeFileDigest bubbles file errors", func(t *testing.T) {
		if err := writeFileDigest(&cacheFailAfterWriter{}, filepath.Join(dir, cacheMissingFileName)); err == nil {
			t.Fatalf("expected writeFileDigest to fail for missing file")
		}
	})
}

func TestAnalysisCacheAdditionalStoreBranches(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based cache write failures are not portable on windows")
	}

	for _, tc := range []cacheStoreFailureCase{
		{name: "object write failure", blockedDir: cacheObjectsDirName, keyDigest: "object-write"},
		{name: "pointer write failure", blockedDir: cacheKeysDirName, keyDigest: "pointer-write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testAnalysisCacheStoreWriteFailure(t, tc)
		})
	}
}

func testAnalysisCacheStoreWriteFailure(t *testing.T, tc cacheStoreFailureCase) {
	t.Helper()

	cachePath := filepath.Join(t.TempDir(), cacheDirName)
	objectsDir := filepath.Join(cachePath, cacheObjectsDirName)
	keysDir := filepath.Join(cachePath, cacheKeysDirName)
	for _, dir := range []string{objectsDir, keysDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	blockedPath := filepath.Join(cachePath, tc.blockedDir)
	if err := os.Chmod(blockedPath, 0o500); err != nil {
		t.Fatalf("chmod %s: %v", blockedPath, err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(blockedPath, 0o700); err != nil {
			t.Fatalf("restore %s perms: %v", blockedPath, err)
		}
	})

	if err := storeTestAnalysisCache(cachePath, tc.keyDigest); err == nil {
		t.Fatalf("expected cache store to fail when %s is not writable", tc.blockedDir)
	}
}

func storeTestAnalysisCache(cachePath, keyDigest string) error {
	entry := cacheEntryDescriptor{KeyDigest: keyDigest, InputDigest: "input"}
	rep := report.Report{RepoPath: "repo"}
	return newTestAnalysisCache(cachePath).store(entry, rep)
}

func newTestAnalysisCache(cachePath string) *analysisCache {
	return &analysisCache{
		options:   resolvedCacheOptions{Enabled: true, Path: cachePath},
		cacheable: true,
	}
}
