package swift

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftPackageCatalogLoadersBoundUntrustedInput(t *testing.T) {
	t.Run("Package.swift size", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), "import PackageDescription\n"+strings.Repeat(" ", maxSwiftPackageManifestBytes))
		found, warnings, err := loadManifestData(repo, &dependencyCatalog{})
		if found || len(warnings) != 0 || !errors.Is(err, safeio.ErrFileTooLarge) {
			t.Fatalf("expected Package.swift byte limit, found=%v warnings=%#v err=%v", found, warnings, err)
		}
	})

	t.Run("Package.resolved size", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), "{\"pins\":[]}"+strings.Repeat(" ", maxSwiftPackageResolvedBytes))
		found, warnings, err := loadResolvedData(repo, &dependencyCatalog{})
		if found || len(warnings) != 0 || !errors.Is(err, safeio.ErrFileTooLarge) {
			t.Fatalf("expected Package.resolved byte limit, found=%v warnings=%#v err=%v", found, warnings, err)
		}
	})

}

func TestSwiftResolvedPinBudget(t *testing.T) {
	content := []byte(`{"pins":[{"identity":"modern"}],"object":{"pins":[{"package":"Legacy"}]}}`)
	if pins, err := parseResolvedPins(content, 2); len(pins) != 2 || err != nil {
		t.Fatalf("expected two pins at the limit, got %#v and %v", pins, err)
	}
	if pins, err := parseResolvedPins(content, 1); len(pins) != 0 || !errors.Is(err, errResolvedPinLimit) {
		t.Fatalf("expected the shared pin limit, got %#v and %v", pins, err)
	}
	if _, err := parseResolvedPins([]byte(`{"pins":{}}`), 2); err == nil {
		t.Fatal("expected an error for pins outside an array")
	}
	if _, err := parseResolvedPins([]byte(`{"pins":[{"identity":1}]}`), 2); err == nil {
		t.Fatal("expected an error for an invalid pin")
	}
	if pins, err := parseResolvedPins([]byte(`{"pins":null,"object":{"pins":[]}}`), 2); len(pins) != 0 || err != nil {
		t.Fatalf("expected no pins for empty and null arrays, got %#v and %v", pins, err)
	}
}

func TestSwiftPackageCatalogKeepsResolvedSourceAfterManifestLimit(t *testing.T) {
	repo := t.TempDir()
	catalog, _, err := buildDependencyCatalog(repo)
	if err != nil {
		t.Fatal(err)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName),
		`{"pins":[{"identity":"example","state":{"version":"1.2.3"}}]}`)
	manifest := loadBoundedCatalogSource(packageManifestName, "SwiftPM", report.CoverageGapSwiftPackageCatalogLimit,
		func(string, *dependencyCatalog) (bool, []string, error) {
			return false, nil, safeio.ErrFileTooLarge
		}, safeio.ErrFileTooLarge)
	state, warnings, err := loadPackageManagerCatalog(repo, &catalog, manifest, loadResolvedData)
	if err != nil || !state.ManifestFound || !state.LockFound {
		t.Fatalf("expected both catalog sources to remain visible, got %#v and %v", state, err)
	}
	if dep := catalog.Dependencies["example"]; !dep.ResolvedViaSwiftPM || dep.Version != "1.2.3" {
		t.Fatalf("expected the resolved dependency, got %#v", dep)
	}
	if len(catalog.CoverageGaps) != 1 || len(warnings) != 1 {
		t.Fatalf("expected one coverage gap and warning, got %#v and %#v", catalog.CoverageGaps, warnings)
	}
	gap := catalog.CoverageGaps[0]
	if gap.Code != report.CoverageGapSwiftPackageCatalogLimit || gap.Language != swiftAdapterID ||
		gap.Path != packageManifestName || len(gap.Evidence) != 1 || gap.Evidence[0] != warnings[0] {
		t.Fatalf("expected a SwiftPM coverage gap with its warning, got %#v", gap)
	}
}
