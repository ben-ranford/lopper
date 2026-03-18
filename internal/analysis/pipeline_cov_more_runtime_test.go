package analysis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestAnalysisPipelineAdditionalSetupBranches(t *testing.T) {
	service := &Service{Registry: language.NewRegistry()}
	invalidPattern := string([]byte{0xff})

	if _, err := service.newAnalysisPipeline(context.Background(), Request{
		RepoPath:        ".",
		IncludePatterns: []string{invalidPattern},
	}); err == nil {
		t.Fatalf("expected newAnalysisPipeline to surface applyPathScope failures")
	}
}

func TestScopedCandidateRootsChangedPackagesSuccessBranch(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))

	roots, warnings := scopedCandidateRoots(ScopeModeChangedPackages, []string{repoRoot}, repoRoot)
	if len(warnings) != 0 {
		t.Fatalf("expected changed-packages resolution without warnings, got %#v", warnings)
	}
	if len(roots) != 1 || roots[0] != repoRoot {
		t.Fatalf("expected changed-packages scope to keep repo root, got %#v", roots)
	}
}
