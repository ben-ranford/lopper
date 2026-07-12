package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestSonarCloudAutomaticAnalysisExcludesOnlyIntentionalJSFixtures(t *testing.T) {
	t.Parallel()

	const want = "sonar.exclusions=testdata/js/cjs/index.cjs,testdata/js/esm/index.js"
	got := strings.ReplaceAll(readConfig(t, ".sonarcloud.properties"), "\r\n", "\n")
	got = strings.TrimSuffix(got, "\n")
	if got != want {
		t.Fatalf(".sonarcloud.properties = %q, want %q", got, want)
	}

	for _, path := range []string{
		"testdata/js/cjs/index.cjs",
		"testdata/js/esm/index.js",
	} {
		if _, err := os.Stat(repoPath(t, path)); err != nil {
			t.Fatalf("stat excluded fixture %s: %v", path, err)
		}
	}
}
