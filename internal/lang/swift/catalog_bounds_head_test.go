package swift

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftReadLimitsMatchRegressionFixtures(t *testing.T) {
	if maxSwiftPackageManifestBytes != regressionSwiftManifestBytes ||
		maxSwiftPackageResolvedBytes != regressionSwiftResolvedBytes ||
		maxPodManifestBytes != regressionPodManifestBytes ||
		maxPodLockBytes != regressionPodLockBytes ||
		maxCarthageManifestBytes != regressionCarthageManifestBytes ||
		maxCarthageResolvedBytes != regressionCarthageResolvedBytes {
		t.Fatal("catalog byte limits drifted from bounded_read_regression_test.go")
	}
}

func TestSwiftCatalogReadDistinguishesFailureKinds(t *testing.T) {
	repo := t.TempDir()
	catalog := newTestSwiftCatalog()
	testutil.MustWriteFile(t, filepath.Join(repo, podLockName), "PODS: [\n")
	if _, _, err := loadPodLockData(repo, &catalog); err == nil || errors.Is(err, safeio.ErrFileTooLarge) || errors.Is(err, errPodLockEntryLimit) || !strings.Contains(err.Error(), "parse ") {
		t.Fatalf("expected invalid Podfile.lock to stay a parse failure, got %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.lock")
	testutil.MustWriteFile(t, outside, "PODS: []\n")
	link := filepath.Join(repo, "Linked.lock")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := readBoundedCatalogFile(repo, link, maxPodLockBytes); !errors.Is(err, safeio.ErrTargetPathSymlink) || errors.Is(err, errInputNotRegular) {
		t.Fatalf("expected symlink to stay contained, got %v", err)
	}

	nested := filepath.Join(repo, "Nested", podLockName)
	testutil.MustWriteFile(t, nested, "PODS: []\n")
	data, err := readBoundedCatalogFile(repo, nested, maxPodLockBytes)
	if err != nil || !strings.Contains(string(data), "PODS:") {
		t.Fatalf("expected nested catalog read to stay inside the root, data=%q err=%v", data, err)
	}

	fileAsRoot := filepath.Join(repo, "not-a-root")
	testutil.MustWriteFile(t, fileAsRoot, "x")
	if _, err := readBoundedCatalogFile(fileAsRoot, filepath.Join(fileAsRoot, podLockName), 32); err == nil {
		t.Fatal("expected a file root to fail before reading")
	}
}

func TestSwiftPodfileRejectsDeclarationLimit(t *testing.T) {
	repo := t.TempDir()
	var body strings.Builder
	for i := 0; i < maxPodDeclarations+1; i++ {
		fmt.Fprintf(&body, "pod 'Pod%d'\n", i)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, podManifestName), body.String())
	catalog := newTestSwiftCatalog()
	if _, _, err := loadPodManifestData(repo, &catalog); !errors.Is(err, errPodDeclarationLimit) || errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected declaration limit, got %v", err)
	}
}

func TestSwiftPodLockRejectsEntryLimit(t *testing.T) {
	repo := t.TempDir()
	var body strings.Builder
	body.WriteString("PODS:\n")
	for i := 0; i < maxPodLockEntries+1; i++ {
		fmt.Fprintf(&body, "  - 'Pod%d (1.0.0)'\n", i)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, podLockName), body.String())
	catalog := newTestSwiftCatalog()
	if _, _, err := loadPodLockData(repo, &catalog); !errors.Is(err, errPodLockEntryLimit) || errors.Is(err, safeio.ErrFileTooLarge) || strings.Contains(err.Error(), "parse ") {
		t.Fatalf("expected pod entry limit distinct from parse and size failures, got %v", err)
	}
}

func TestSwiftPackageResolvedRejectsPinLimit(t *testing.T) {
	repo := t.TempDir()
	var body strings.Builder
	body.WriteString(`{"pins":[`)
	for i := 0; i < maxResolvedPins+1; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		fmt.Fprintf(&body, `{"identity":"pkg%d","state":{"version":"1.0.0"}}`, i)
	}
	body.WriteString(`]}`)
	testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), body.String())
	catalog := newTestSwiftCatalog()
	if _, _, err := loadResolvedData(repo, &catalog); !errors.Is(err, errResolvedPinLimit) || errors.Is(err, safeio.ErrFileTooLarge) || strings.Contains(err.Error(), "parse ") {
		t.Fatalf("expected pin limit distinct from parse and size failures, got %v", err)
	}
}
