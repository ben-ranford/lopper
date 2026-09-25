package kotlinandroid

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKotlinAndroidDetectionFollowupBranches(t *testing.T) {
	repo := t.TempDir()

	buildFile := filepath.Join(repo, buildGradleName)
	if err := os.WriteFile(buildFile, []byte(`plugins { id("com.android.application") }`), 0o644); err != nil {
		t.Fatalf("write build file: %v", err)
	}
	if !buildFileSignalsAndroidPlugin("", buildFile) {
		t.Fatalf("expected empty-repoPath branch to read the build file directly")
	}

	if hasRootSourceLayout(filepath.Join(repo, "missing")) {
		t.Fatalf("expected missing root source layout scan to return false")
	}

	if isSubPath(repo, "relative/path") {
		t.Fatalf("expected mixed absolute/relative paths to return false")
	}
}
