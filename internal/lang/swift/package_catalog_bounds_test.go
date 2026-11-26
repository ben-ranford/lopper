package swift

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftPackageManifestReadHonorsByteLimit(t *testing.T) {
	const manifestLimit = 2 * 1024 * 1024
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), "// "+strings.Repeat(" ", manifestLimit))
	found, _, err := loadManifestData(repo, &dependencyCatalog{})
	if found || !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected size error for an over-limit Swift manifest, got found=%v and err=%v", found, err)
	}
}
