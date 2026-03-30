package kotlinandroid

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestKotlinAndroidDetectionFollowupBranches(t *testing.T) {
	repo := t.TempDir()

	appDir := filepath.Join(repo, "app")
	if err := os.Mkdir(appDir, 0o755); err != nil {
		t.Fatalf("mkdir app dir: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one directory entry, got %d", len(entries))
	}

	visited := 0
	detection := language.Detection{}
	state := detectionWalkState{
		repoPath:              repo,
		roots:                 map[string]struct{}{},
		detection:             &detection,
		visited:               &visited,
		maxFiles:              10,
		androidSpecificSignal: new(bool),
	}
	if err := walkKotlinAndroidDetectionEntry(appDir, entries[0], state); err != nil {
		t.Fatalf("walkKotlinAndroidDetectionEntry for regular dir: %v", err)
	}

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
