package scripts

import (
	"os"
	"testing"
)

func TestSonarCloudAutomaticAnalysisExcludesOnlyIntentionalJSFixtures(t *testing.T) {
	t.Parallel()

	const want = "sonar.exclusions=testdata/js/cjs/index.cjs,testdata/js/esm/index.js\n"
	if got := readConfig(t, ".sonarcloud.properties"); got != want {
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
