package swift

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestCocoaPodsManifestReadHonorsByteLimit(t *testing.T) {
	const manifestLimit = 2 * 1024 * 1024
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, podManifestName), "# "+strings.Repeat(" ", manifestLimit))
	found, _, err := loadPodManifestData(repo, &dependencyCatalog{})
	if found || !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected size error for an over-limit Podfile, got found=%v and err=%v", found, err)
	}
}
