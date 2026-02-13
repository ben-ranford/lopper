package dotnet

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	newtonsoftProjectManifest = `
<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Newtonsoft.Json" Version="13.0.3" />
  </ItemGroup>
</Project>`
	dapperProjectManifest = `
<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Dapper" Version="2.1.35" />
  </ItemGroup>
</Project>`
	serilogCentralManifest = `
<Project>
  <ItemGroup>
    <PackageVersion Include="Serilog.AspNetCore" Version="8.0.0" />
  </ItemGroup>
</Project>`
	appProjectFileName          = "App.csproj"
	centralPackagesFileName     = "Directory.Packages.props"
	programSourceFileName       = "Program.cs"
	newtonsoftDependencyName    = "newtonsoft.json"
	serilogDependencyName       = "serilog.aspnetcore"
	expectedSingleDependencyFmt = "expected one dependency report, got %d"
)

func writeManifestFixture(t *testing.T, path string, content string) {
	t.Helper()
	testutil.MustWriteFile(t, path, content)
}

func expectSingleDotNetDependency(t *testing.T, deps []report.DependencyReport) report.DependencyReport {
	t.Helper()
	if len(deps) != 1 {
		t.Fatalf(expectedSingleDependencyFmt, len(deps))
	}
	dep := deps[0]
	if dep.Language != "dotnet" {
		t.Fatalf("expected language dotnet, got %q", dep.Language)
	}
	return dep
}

func TestAdapterDetectWithSolutionAndProjects(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "App.sln"), `
Microsoft Visual Studio Solution File, Format Version 12.00
Project("{FAKE}") = "App", "src/App/App.csproj", "{ONE}"
EndProject
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "App", appProjectFileName), `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, centralPackagesFileName), `<Project><ItemGroup><PackageVersion Include="Newtonsoft.Json" Version="13.0.3"/></ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "App", programSourceFileName), "using Newtonsoft.Json;\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected dotnet detection to match")
	}
	if detection.Confidence <= 0 {
		t.Fatalf("expected confidence > 0, got %d", detection.Confidence)
	}
	foundRoot := false
	for _, root := range detection.Roots {
		if filepath.Base(root) == "App" {
			foundRoot = true
			break
		}
	}
	if !foundRoot {
		t.Fatalf("expected project root in detection roots, got %#v", detection.Roots)
	}
}

func TestAdapterAnalyseDependency(t *testing.T) {
	repo := t.TempDir()
	writeManifestFixture(t, filepath.Join(repo, "Api.csproj"), newtonsoftProjectManifest)
	testutil.MustWriteFile(t, filepath.Join(repo, programSourceFileName), `
using Newtonsoft.Json;
using JsonConvert = Newtonsoft.Json.JsonConvert;

public class Program {
  public static void Main() {
    _ = JsonConvert.SerializeObject(new { Name = "demo" });
  }
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: newtonsoftDependencyName,
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	dep := expectSingleDotNetDependency(t, reportData.Dependencies)
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected used exports > 0")
	}
}

func TestAdapterAnalyseTopNWithCentralPackages(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, centralPackagesFileName), `
<Project>
  <ItemGroup>
    <PackageVersion Include="Serilog.AspNetCore" Version="8.0.0" />
    <PackageVersion Include="Dapper" Version="2.1.35" />
  </ItemGroup>
</Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "Api", "Api.csproj"), `
<Project Sdk="Microsoft.NET.Sdk.Web">
  <ItemGroup>
    <PackageReference Include="Serilog.AspNetCore" />
    <PackageReference Include="Dapper" />
  </ItemGroup>
</Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "Api", programSourceFileName), `
using Serilog;

public class Program {
  public static void Main() {
    Log.Information("hello");
  }
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	if err != nil {
		t.Fatalf("analyse top: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies in top-N report")
	}
	names := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, serilogDependencyName) {
		t.Fatalf("expected serilog.aspnetcore in top dependencies, got %#v", names)
	}
	if !slices.Contains(names, "dapper") {
		t.Fatalf("expected dapper in top dependencies, got %#v", names)
	}
}

func TestAdapterMetadataAndAliases(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "dotnet" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	aliases := adapter.Aliases()
	if !slices.Contains(aliases, "csharp") || !slices.Contains(aliases, "fsharp") {
		t.Fatalf("unexpected adapter aliases: %#v", aliases)
	}
}

func TestCollectDeclaredDependencies(t *testing.T) {
	repo := t.TempDir()
	writeManifestFixture(t, filepath.Join(repo, centralPackagesFileName), serilogCentralManifest)
	writeManifestFixture(t, filepath.Join(repo, appProjectFileName), dapperProjectManifest)

	deps, err := collectDeclaredDependencies(repo)
	if err != nil {
		t.Fatalf("collect dependencies: %v", err)
	}
	if !slices.Contains(deps, serilogDependencyName) {
		t.Fatalf("expected serilog.aspnetcore in deps, got %#v", deps)
	}
	if !slices.Contains(deps, "dapper") {
		t.Fatalf("expected dapper in deps, got %#v", deps)
	}
}

func TestParseManifestReferences(t *testing.T) {
	repo := t.TempDir()
	propsPath := filepath.Join(repo, centralPackagesFileName)
	projectPath := filepath.Join(repo, appProjectFileName)
	writeManifestFixture(t, propsPath, serilogCentralManifest)
	writeManifestFixture(t, projectPath, dapperProjectManifest)

	propsDeps, err := parsePackageVersions(repo, propsPath)
	if err != nil {
		t.Fatalf("parse props: %v", err)
	}
	if !slices.Contains(propsDeps, serilogDependencyName) {
		t.Fatalf("expected serilog.aspnetcore in props deps, got %#v", propsDeps)
	}

	projectDeps, err := parsePackageReferences(repo, projectPath)
	if err != nil {
		t.Fatalf("parse project: %v", err)
	}
	if !slices.Contains(projectDeps, "dapper") {
		t.Fatalf("expected dapper in project deps, got %#v", projectDeps)
	}
}

func TestAdapterRecommendationsHonorMinUsageThreshold(t *testing.T) {
	repo := t.TempDir()
	writeManifestFixture(t, filepath.Join(repo, appProjectFileName), newtonsoftProjectManifest)
	testutil.MustWriteFile(t, filepath.Join(repo, programSourceFileName), `
using Newtonsoft.Json;

public class Program {
  public static void Main() {}
}
`)

	withDefaultThreshold, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: newtonsoftDependencyName,
	})
	if err != nil {
		t.Fatalf("analyse with default threshold: %v", err)
	}
	depDefault := expectSingleDotNetDependency(t, withDefaultThreshold.Dependencies)
	if !hasRecommendation(depDefault, "reduce-low-usage-package-surface") {
		t.Fatalf("expected low-usage recommendation with default threshold")
	}

	zero := 0
	withZeroThreshold, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:                          repo,
		Dependency:                        newtonsoftDependencyName,
		MinUsagePercentForRecommendations: &zero,
	})
	if err != nil {
		t.Fatalf("analyse with zero threshold: %v", err)
	}
	depZero := expectSingleDotNetDependency(t, withZeroThreshold.Dependencies)
	if hasRecommendation(depZero, "reduce-low-usage-package-surface") {
		t.Fatalf("did not expect low-usage recommendation when threshold is 0")
	}
}

func hasRecommendation(dep report.DependencyReport, code string) bool {
	for _, rec := range dep.Recommendations {
		if rec.Code == code {
			return true
		}
	}
	return false
}
