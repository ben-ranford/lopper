package analysis

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestAnalysisCacheSeparatesNormalizedScopeModes(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "index.js"), "console.log('scope cache')\n")
	svc, adapter := newCacheTestService(t)
	req := newCacheRequest(t, repo, filepath.Join(t.TempDir(), "cache"), false)
	for _, step := range []struct {
		mode  string
		calls int
	}{
		{"", 1},
		{" PACKAGE ", 1},
		{ScopeModeRepo, 2},
		{" REPO ", 2},
	} {
		req.ScopeMode = step.mode
		if _, err := svc.Analyse(context.Background(), req); err != nil {
			t.Fatalf("analyse scope %q: %v", step.mode, err)
		}
		if adapter.calls != step.calls {
			t.Fatalf("scope %q: adapter calls = %d, want %d", step.mode, adapter.calls, step.calls)
		}
	}
}

func TestAnalysisCachePreservesDotNetProjectsAcrossScopeChanges(t *testing.T) {
	for _, firstMode := range []string{ScopeModePackage, ScopeModeRepo} {
		t.Run(firstMode, func(t *testing.T) {
			repo := t.TempDir()
			for _, project := range []struct {
				dir  string
				name string
			}{{".", "Root.Package"}, {"nested", "Nested.Package"}} {
				writeFile(t, filepath.Join(repo, project.dir, "Project.csproj"), `<Project><ItemGroup><PackageReference Include="`+project.name+`" /></ItemGroup></Project>`)
				writeFile(t, filepath.Join(repo, project.dir, "Program.cs"), "using "+project.name+";\n")
			}
			req := newCacheRequest(t, repo, filepath.Join(t.TempDir(), "cache"), false)
			req.Language = "dotnet"
			req.TopN = 2
			secondMode := ScopeModeRepo
			if firstMode == ScopeModeRepo {
				secondMode = ScopeModePackage
			}
			for _, mode := range []string{firstMode, secondMode, secondMode} {
				req.ScopeMode = mode
				result, err := NewService().Analyse(context.Background(), req)
				if err != nil {
					t.Fatalf("analyse scope %q: %v", mode, err)
				}
				assertCachedDotNetProjectUsage(t, mode, result)
			}
		})
	}
}

func assertCachedDotNetProjectUsage(t *testing.T, mode string, result report.Report) {
	t.Helper()
	if len(result.Dependencies) != 2 {
		t.Fatalf("scope %q lost a project: %#v", mode, result.Dependencies)
	}
	for _, dependency := range result.Dependencies {
		if dependency.UsedExportsCount != 1 || len(dependency.UsedImports) != 1 || len(dependency.UsedImports[0].Locations) != 1 {
			t.Fatalf("scope %q duplicated or lost usage: %#v", mode, dependency)
		}
	}
}
