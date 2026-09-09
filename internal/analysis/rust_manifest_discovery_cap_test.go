package analysis

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestServiceAnalyseRustManifestDiscoveryCapReportsIncompleteCoverage(t *testing.T) {
	for _, testCase := range []rustManifestDiscoveryCapCase{
		{name: "repo-members-255", memberCount: 255, scopeMode: ScopeModeRepo, expectedUsage: 1},
		{name: "repo-members-256", memberCount: 256, scopeMode: ScopeModeRepo, capped: true},
		{name: "package-members-255", memberCount: 255, scopeMode: ScopeModePackage, expectedUsage: 1},
		{name: "package-members-256", memberCount: 256, scopeMode: ScopeModePackage, expectedUsage: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo := writeRustWorkspaceWithMembers(t, testCase.memberCount)
			reportData, err := analyseRustManifestCapWorkspace(repo, testCase.scopeMode, false)
			if err != nil {
				t.Fatalf("analyse Rust workspace: %v", err)
			}
			assertRustManifestDiscoveryCapReport(t, reportData, testCase)

			_, err = analyseRustManifestCapWorkspace(repo, testCase.scopeMode, true)
			assertRustManifestDiscoveryCapCoverage(t, err, testCase.capped)
		})
	}
}

type rustManifestDiscoveryCapCase struct {
	name          string
	memberCount   int
	scopeMode     string
	capped        bool
	expectedUsage int
}

func analyseRustManifestCapWorkspace(repo, scopeMode string, requireCompleteCoverage bool) (report.Report, error) {
	return NewService().Analyse(context.Background(), Request{
		RepoPath:                repo,
		Language:                "rust",
		ScopeMode:               scopeMode,
		Dependency:              "tail",
		RequireCompleteCoverage: requireCompleteCoverage,
		Cache:                   &CacheOptions{Enabled: false},
	})
}

func assertRustManifestDiscoveryCapReport(t *testing.T, reportData report.Report, testCase rustManifestDiscoveryCapCase) {
	t.Helper()
	if testCase.capped {
		if len(reportData.CoverageGaps) != 1 || reportData.CoverageGaps[0].Code != "rust-manifest-discovery-truncated" || reportData.CoverageGaps[0].Path != "." {
			t.Fatalf("expected manifest discovery coverage gap, got %#v", reportData.CoverageGaps)
		}
		if !containsWarning(reportData.Warnings, "cargo manifest discovery capped at 256 manifests") {
			t.Fatalf("expected manifest cap warning, got %#v", reportData.Warnings)
		}
	} else if len(reportData.CoverageGaps) != 0 {
		t.Fatalf("complete member analysis produced coverage gaps: %#v", reportData.CoverageGaps)
	}
	if len(reportData.Dependencies) != 1 || reportData.Dependencies[0].Name != "tail" || reportData.Dependencies[0].UsedExportsCount != testCase.expectedUsage {
		t.Fatalf("expected tail usage %d, got %#v", testCase.expectedUsage, reportData.Dependencies)
	}
}

func assertRustManifestDiscoveryCapCoverage(t *testing.T, err error, capped bool) {
	t.Helper()
	if capped {
		if !errors.Is(err, ErrIncompleteCoverage) {
			t.Fatalf("expected capped manifest discovery to fail complete coverage, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("complete member analysis should satisfy complete coverage: %v", err)
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
