package scripts

import (
	"path/filepath"
	"strings"
	"testing"
)

// copyBenchGateSafeioSources includes safeio's canonical internal dependency
// whenever a benchmark fixture builds the real benchdelta helper.
func copyBenchGateSafeioSources(t *testing.T, repo string) {
	t.Helper()
	for _, packagePath := range []string{"internal/safeio", "internal/errutil"} {
		files, err := filepath.Glob(repoPath(t, packagePath+"/*.go"))
		if err != nil {
			t.Fatalf("glob %s sources: %v", packagePath, err)
		}
		for _, sourcePath := range files {
			if strings.HasSuffix(sourcePath, "_test.go") {
				continue
			}
			rel := filepath.Join(packagePath, filepath.Base(sourcePath))
			writeFile(t, filepath.Join(repo, rel), readConfig(t, rel))
		}
	}
}
