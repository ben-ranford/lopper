package swift

import (
	"errors"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
	"path/filepath"
	"strings"
	"testing"
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

	t.Run("Package.resolved pins", func(t *testing.T) {
		var content strings.Builder
		content.WriteString("{\"pins\":[")
		for i := 0; i <= maxResolvedPins; i++ {
			if i > 0 {
				content.WriteByte(',')
			}
			content.WriteString("{}")
		}
		content.WriteString("]}")
		if _, err := parseResolvedPins([]byte(content.String())); !errors.Is(err, errResolvedPinLimit) {
			t.Fatalf("expected Package.resolved pin limit, got %v", err)
		}
	})
}
