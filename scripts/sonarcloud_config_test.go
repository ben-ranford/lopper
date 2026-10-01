package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestSonarCloudAutomaticAnalysisIncludesJSFixtures(t *testing.T) {
	t.Parallel()

	if got := strings.TrimSpace(readConfig(t, ".sonarcloud.properties")); got != "" {
		t.Fatalf(".sonarcloud.properties must not exclude sources: got %q", got)
	}

	for _, path := range []string{
		"testdata/js/cjs/index.cjs",
		"testdata/js/esm/index.js",
	} {
		if _, err := os.Stat(repoPath(t, path)); err != nil {
			t.Fatalf("stat included fixture %s: %v", path, err)
		}
	}
}
