package analysis

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestServiceAnalyseRustManifestDiscoveryCapReportsIncompleteCoverage(t *testing.T) {
	for _, memberCount := range []int{255, 256} {
		for _, scopeMode := range []string{ScopeModeRepo, ScopeModePackage} {
			t.Run(fmt.Sprintf("%s-members-%d", scopeMode, memberCount), func(t *testing.T) {
				repo := writeRustWorkspaceWithMembers(t, memberCount)
				reportData, err := NewService().Analyse(context.Background(), Request{
					RepoPath:   repo,
					Language:   "rust",
					ScopeMode:  scopeMode,
					Dependency: "tail",
					Cache:      &CacheOptions{Enabled: false},
				})
				if err != nil {
					t.Fatalf("analyse Rust workspace: %v", err)
				}
				cappedRepository := memberCount == 256 && scopeMode == ScopeModeRepo
				if !cappedRepository {
					if len(reportData.CoverageGaps) != 0 {
						t.Fatalf("complete member analysis produced coverage gaps: %#v", reportData.CoverageGaps)
					}
				} else {
					if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Code != "rust-manifest-discovery-truncated" || reportData.CoverageGaps[0].Path != "." {
						t.Fatalf("expected manifest discovery coverage gap, got %#v", reportData.CoverageGaps)
					}
					if !containsWarning(reportData.Warnings, "cargo manifest discovery capped at 256 manifests") {
						t.Fatalf("expected manifest cap warning, got %#v", reportData.Warnings)
					}
				}
				if !cappedRepository {
					if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].Name != "tail" || reportData.Dependencies[0].UsedExportsCount != 1 {
						t.Fatalf("expected final workspace member dependency usage, got %#v", reportData.Dependencies)
					}
				} else if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].Name != "tail" || reportData.Dependencies[0].UsedExportsCount != 0 {
					t.Fatalf("expected capped repository scan to retain an unused requested dependency, got %#v", reportData.Dependencies)
				}

				_, err = NewService().Analyse(context.Background(), Request{
					RepoPath:                repo,
					Language:                "rust",
					ScopeMode:               scopeMode,
					Dependency:              "tail",
					RequireCompleteCoverage: true,
					Cache:                   &CacheOptions{Enabled: false},
				})
				if !cappedRepository {
					if err != nil {
						t.Fatalf("complete member analysis should satisfy complete coverage: %v", err)
					}
				} else if !errors.Is(err, ErrIncompleteCoverage) {
					t.Fatalf("expected capped manifest discovery to fail complete coverage, got %v", err)
				}
			})
		}
	}
}

func writeRustWorkspaceWithMembers(t *testing.T, memberCount int) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Cargo.toml"), "[workspace]\nmembers = [\"crates/*\"]\n")
	for index := range memberCount {
		name := fmt.Sprintf("crate-%03d", index)
		manifest := fmt.Sprintf("[package]\nname = %q\nversion = \"0.1.0\"\n", name)
		if index == memberCount-1 {
			manifest += "\n[dependencies]\ntail = \"1\"\n"
		}
		crateRoot := filepath.Join(repo, "crates", name)
		writeFile(t, filepath.Join(crateRoot, "Cargo.toml"), manifest)
		if index == memberCount-1 {
			writeFile(t, filepath.Join(crateRoot, "src", "lib.rs"), "use tail::Thing;\npub fn use_tail() { let _ = Thing; }\n")
		}
	}
	return repo
}
