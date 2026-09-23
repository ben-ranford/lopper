package kotlinandroid

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestDiscoverGradleFilesBoundsRetainedContent(t *testing.T) {
	const (
		retainedContentBudget = 16 * 1024 * 1024
		fixtureFileSize       = 1024 * 1024
		fixtureFileCount      = retainedContentBudget/fixtureFileSize + 4
	)

	repo := t.TempDir()
	content := strings.Repeat("x", fixtureFileSize)
	for i := 0; i < fixtureFileCount; i++ {
		path := filepath.Join(repo, fmt.Sprintf("module-%02d", i), buildGradleName)
		testutil.MustWriteFile(t, path, content)
	}

	discovery, err := discoverBuildFiles(repo, buildGradleName)
	if err != nil {
		t.Fatalf("discover build files: %v", err)
	}

	retained := 0
	for _, file := range discovery.Files {
		retained += len(file.Content)
	}
	if retained > retainedContentBudget {
		t.Fatalf("discovery retained %d bytes, exceeding the %d-byte content budget", retained, retainedContentBudget)
	}
	if len(discovery.Warnings) == 0 {
		t.Fatal("expected a warning when the aggregate content budget is reached")
	}
}
