package swift

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	regressionSwiftManifestBytes    = 2 << 20
	regressionSwiftResolvedBytes    = 8 << 20
	regressionPodManifestBytes      = 2 << 20
	regressionPodLockBytes          = 8 << 20
	regressionCarthageManifestBytes = 1 << 20
	regressionCarthageResolvedBytes = 2 << 20
)

func TestSwiftCocoaPodsLockfileRejectsOversizeInput(t *testing.T) {
	assertOversizeCatalogRead(t, podLockName, "PODS: []\n", regressionPodLockBytes, true, loadPodLockData)
}

func TestSwiftCocoaPodsManifestRejectsOversizeInput(t *testing.T) {
	assertOversizeCatalogRead(t, podManifestName, "pod 'Alamofire'\n", regressionPodManifestBytes, false, loadPodManifestData)
}

func TestSwiftPackageManifestRejectsOversizeInput(t *testing.T) {
	assertOversizeCatalogRead(t, packageManifestName, "import PackageDescription\n", regressionSwiftManifestBytes, false, loadManifestData)
}

func TestSwiftPackageResolvedRejectsOversizeInput(t *testing.T) {
	assertOversizeCatalogRead(t, packageResolvedName, "{\"pins\":[]}\n", regressionSwiftResolvedBytes, false, loadResolvedData)
}

func TestSwiftCarthageFilesRejectOversizeInput(t *testing.T) {
	assertOversizeCatalogRead(t, carthageManifestName, "github \"owner/demo\"\n", regressionCarthageManifestBytes, false, loadCarthageManifestData)
	assertOversizeCatalogRead(t, carthageResolvedName, "github \"owner/demo\" \"1.0.0\"\n", regressionCarthageResolvedBytes, false, loadCarthageResolvedData)
}

func TestSwiftSourceReadBoundsStatLie(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "Big.swift")
	testutil.MustWriteFile(t, path, strings.Repeat("a", maxScannableSwiftFile+1))
	entries, err := os.ReadDir(repo)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read source dir: %v entries=%d", err, len(entries))
	}
	scanner := &repoScanner{repoPath: repo}
	if err := scanner.scanSwiftFile(path, staticSizeEntry{DirEntry: entries[0], size: 1}); err != nil {
		t.Fatalf("scan oversized source: %v", err)
	}
	if scanner.skippedLargeFiles != 1 || len(scanner.scan.Files) != 0 {
		t.Fatalf("expected the read limit to skip the file, skipped=%d files=%d", scanner.skippedLargeFiles, len(scanner.scan.Files))
	}
}

func assertOversizeCatalogRead(t *testing.T, name, prefix string, limit int, measureAlloc bool, load func(string, *dependencyCatalog) (bool, []string, error)) {
	t.Helper()
	if len(prefix) > limit {
		t.Fatalf("prefix longer than %s limit", name)
	}
	repo := t.TempDir()
	body := prefix + strings.Repeat(" ", limit-len(prefix)+1)
	testutil.MustWriteFile(t, filepath.Join(repo, name), body)
	catalog := newTestSwiftCatalog()
	var before runtime.MemStats
	if measureAlloc {
		runtime.GC()
		runtime.ReadMemStats(&before)
	}
	found, warnings, err := load(repo, &catalog)
	if measureAlloc {
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		if after.TotalAlloc-before.TotalAlloc > 1<<20 {
			t.Fatalf("oversize %s reject allocated %d bytes", name, after.TotalAlloc-before.TotalAlloc)
		}
	}
	if found || len(warnings) != 0 || !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversize %s to fail closed with ErrFileTooLarge, found=%v warnings=%#v err=%v", name, found, warnings, err)
	}
	if strings.Contains(err.Error(), "parse ") {
		t.Fatalf("oversize %s reported as a parse failure: %v", name, err)
	}
}

type staticSizeEntry struct {
	fs.DirEntry
	size int64
}

func (s staticSizeEntry) Info() (fs.FileInfo, error) {
	info, err := s.DirEntry.Info()
	if err != nil {
		return nil, err
	}
	return staticSizeInfo{FileInfo: info, size: s.size}, nil
}

type staticSizeInfo struct {
	fs.FileInfo
	size int64
}

func (s staticSizeInfo) Size() int64 { return s.size }
