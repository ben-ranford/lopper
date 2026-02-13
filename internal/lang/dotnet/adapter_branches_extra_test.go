package dotnet

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	appProjectName      = "App.csproj"
	programSourceName   = "Program.cs"
	newtonsoftName      = "newtonsoft.json"
	acmeBarName         = "acme.bar"
	acmeLoggingName     = "acme.logging"
	acmeFooSourceImport = "using Acme.Foo;\n"
)

func TestDetectAndRootSignalBranches(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, appProjectName), `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "App.sln"),
		"Project(\"{FAKE}\") = \"App\", \"src\\\\App\\\\"+appProjectName+"\", \"{ONE}\"\nEndProject\n",
	)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "App", appProjectName), `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "App", programSourceName), "using System;\n")

	ok, err := NewAdapter().Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect=true for csharp repo")
	}

	detection := language.Detection{}
	roots := map[string]struct{}{}
	if applyRootSignals(filepath.Join(repo, "missing"), &detection, roots) == nil {
		t.Fatalf("expected applyRootSignals to fail on missing path")
	}

	detection = language.Detection{}
	roots = map[string]struct{}{}
	if updateDetection(repo, filepath.Join(repo, "broken.sln"), "broken.sln", &detection, roots) == nil {
		t.Fatalf("expected updateDetection to fail for unreadable solution")
	}
}

func TestScanRepoAndReadSourceBranches(t *testing.T) {
	if _, err := scanRepo(context.Background(), ""); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected fs.ErrInvalid for empty repo path, got %v", err)
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Generated.g.cs"), "using Foo;\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "notes.txt"), "x")
	scan, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if scan.SkippedGeneratedFiles == 0 {
		t.Fatalf("expected generated file skip count > 0")
	}
	if !slices.Contains(scan.Warnings, "no C#/F# source files found for analysis") {
		t.Fatalf("expected no source warning, got %#v", scan.Warnings)
	}

	if _, _, err := readSourceFile(repo, filepath.Join(repo, "missing.cs")); err == nil {
		t.Fatalf("expected readSourceFile error for missing file")
	}

	canceled := testutil.CanceledContext()
	testutil.MustWriteFile(t, filepath.Join(repo, programSourceName), "using Foo.Bar;\n")
	if _, err := scanRepo(canceled, repo); err == nil {
		t.Fatalf("expected canceled context error")
	}
}

func TestCollectDeclaredDependenciesAncestorFallback(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "src", "service")
	testutil.MustWriteFile(t, filepath.Join(parent, "Directory.Packages.props"), `
<Project><ItemGroup><PackageVersion Include="Acme.Platform" Version="1.0.0" /></ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "Service.csproj"), `
<Project><ItemGroup><PackageReference Include="Dapper" Version="2.1.35" /></ItemGroup></Project>`)

	deps, err := collectDeclaredDependencies(repo)
	if err != nil {
		t.Fatalf("collect dependencies: %v", err)
	}
	if !slices.Contains(deps, "acme.platform") || !slices.Contains(deps, "dapper") {
		t.Fatalf("expected ancestor and local dependencies, got %#v", deps)
	}
}

func TestDependencyMapperAndScoreBranches(t *testing.T) {
	mapper := newDependencyMapper([]string{acmeBarName, "acme.baz", newtonsoftName})
	assertResolverBranches(t, mapper)
	assertMatchScoreBranches(t)
	assertFallbackBranches(t)
}

func TestParsingHelperBranches(t *testing.T) {
	if mod, alias, ok := parseCSharpUsing("using Json = Newtonsoft.Json;"); !ok || mod != "Newtonsoft.Json" || alias != "Json" {
		t.Fatalf("expected alias using parse")
	}
	if mod, alias, ok := parseCSharpUsing("global using static Newtonsoft.Json.Linq.JObject;"); !ok || alias != "" || mod != "Newtonsoft.Json.Linq.JObject" {
		t.Fatalf("expected global static using parse")
	}
	if _, _, ok := parseCSharpUsing("using ;"); ok {
		t.Fatalf("expected malformed csharp using to fail")
	}

	if mod, ok := parseFSharpOpen("open My.App.Core"); !ok || mod != "My.App.Core" {
		t.Fatalf("expected fsharp open parse")
	}
	if _, ok := parseFSharpOpen("open "); ok {
		t.Fatalf("expected malformed fsharp open to fail")
	}

	if !shouldSkipDir("node_modules") || shouldSkipDir("src") {
		t.Fatalf("unexpected shouldSkipDir behavior")
	}
	if !isGeneratedSource("Foo.Designer.cs") || isGeneratedSource(programSourceName) {
		t.Fatalf("unexpected generated source detection behavior")
	}
	if got := stripLineComment(" using Foo.Bar; // comment "); got != "using Foo.Bar;" {
		t.Fatalf("stripLineComment mismatch: %q", got)
	}
	if got := lastSegment(""); got != "" {
		t.Fatalf("lastSegment empty mismatch: %q", got)
	}
}

func TestParseImportsAndBuildFunctionsBranches(t *testing.T) {
	mapper := newDependencyMapper([]string{acmeBarName, "acme.baz"})
	content := []byte(`
using Logger = Acme.Foo;
using Acme.Foo;
using System.Text;
open Acme.Foo;
`)
	imports, meta := parseImports(content, programSourceName, mapper)
	if len(imports) < 2 {
		t.Fatalf("expected imports parsed, got %#v", imports)
	}
	if meta.ambiguousByDependency[acmeBarName] == 0 {
		t.Fatalf("expected ambiguous mapping count for acme.bar")
	}

	empty := scanResult{
		AmbiguousByDependency:  map[string]int{},
		UndeclaredByDependency: map[string]int{},
	}
	deps, warnings := buildRequestedDotNetDependencies(language.Request{}, empty)
	if len(deps) != 0 || len(warnings) == 0 {
		t.Fatalf("expected missing target warning for buildRequestedDotNetDependencies")
	}
	deps, warnings = buildTopDotNetDependencies(5, empty, 40)
	if len(deps) != 0 || len(warnings) == 0 {
		t.Fatalf("expected empty top-N warning")
	}

	scan := scanResult{
		Files: []fileScan{
			{
				Path: programSourceName,
				Imports: []importBinding{
					{
						Dependency: acmeLoggingName,
						Module:     "Acme.Logging",
						Name:       "Logger",
						Local:      "Logger",
					},
				},
				Usage: map[string]int{"Logger": 1},
			},
		},
		AmbiguousByDependency:  map[string]int{acmeLoggingName: 2},
		UndeclaredByDependency: map[string]int{acmeLoggingName: 1},
	}
	dep, depWarnings := buildDependencyReport(acmeLoggingName, scan, 80)
	if len(dep.RiskCues) < 2 {
		t.Fatalf("expected risk cues for ambiguous + undeclared, got %#v", dep.RiskCues)
	}
	if len(depWarnings) < 2 {
		t.Fatalf("expected warnings for ambiguous + undeclared, got %#v", depWarnings)
	}
	if len(dep.Recommendations) == 0 {
		t.Fatalf("expected recommendations for risky dependency")
	}
}

func TestCaptureMatchesAndSolutionRootsBranches(t *testing.T) {
	if captureMatches(nil) != nil {
		t.Fatalf("expected nil matches result")
	}
	if got := captureMatches([][][]byte{{[]byte("only-one-element")}}); len(got) != 0 {
		t.Fatalf("expected empty result for malformed match set, got %#v", got)
	}

	repo := t.TempDir()
	sln := filepath.Join(repo, "App.sln")
	testutil.MustWriteFile(t, sln, `
Project("{FAKE}") = "App", "src\\App\\App.csproj", "{ONE}"
Project("{FAKE}") = "Lib", "src\\Lib\\Lib.fsproj", "{TWO}"
EndProject
`)
	roots := map[string]struct{}{}
	if err := addSolutionRoots(repo, sln, roots); err != nil {
		t.Fatalf("addSolutionRoots: %v", err)
	}
	if len(roots) < 2 {
		t.Fatalf("expected multiple roots from solution, got %#v", roots)
	}
}

func TestDetectAndScanFileLimitsAndAnalysisWarnings(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, appProjectName), `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
	for i := 0; i < maxScanFiles+10; i++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "src", "f"+strconv.Itoa(i)+".cs"), acmeFooSourceImport)
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with many files: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected detection to match with many files")
	}

	scan, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan with many files: %v", err)
	}
	if !scan.SkippedFileLimit {
		t.Fatalf("expected scan file limit to be triggered")
	}
	if !slices.Contains(scan.Warnings, "source scan capped at 4096 files") {
		t.Fatalf("expected scan cap warning, got %#v", scan.Warnings)
	}

	noManifestRepo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(noManifestRepo, programSourceName), "using Missing.Package;\n")
	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: noManifestRepo,
		TopN:     1,
	})
	if err != nil {
		t.Fatalf("analyse no manifest: %v", err)
	}
	if !slices.Contains(reportData.Warnings, "no .NET package dependencies discovered from project manifests") {
		t.Fatalf("expected no-manifest warning, got %#v", reportData.Warnings)
	}
}

func TestErrorBranchesForContextAndManifestReaders(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "A.csproj"), `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, programSourceName), acmeFooSourceImport)

	canceled := testutil.CanceledContext()
	if _, err := NewAdapter().DetectWithConfidence(canceled, repo); err == nil {
		t.Fatalf("expected detect canceled context error")
	}
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: filepath.Join(repo, "missing"), TopN: 1}); err == nil {
		t.Fatalf("expected analyse error for missing repo")
	}

	if _, err := parsePackageReferences(repo, filepath.Join(repo, "..", "escape.csproj")); err == nil {
		t.Fatalf("expected parsePackageReferences to reject escaped path")
	}
	if _, err := parsePackageVersions(repo, filepath.Join(repo, "..", "escape.props")); err == nil {
		t.Fatalf("expected parsePackageVersions to reject escaped path")
	}
}

func TestUnreadableManifestAndSourceErrorBranches(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, appProjectName), `<Project><ItemGroup><PackageReference Include="A" Version="1.0.0" /></ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, centralPackagesFile), `<Project><ItemGroup><PackageVersion Include="B" Version="1.0.0" /></ItemGroup></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, programSourceName), acmeFooSourceImport)

	if _, err := collectDeclaredDependencies(filepath.Join(repo, "missing-repo")); err == nil {
		t.Fatalf("expected collectDeclaredDependencies error for missing repo")
	}
	if _, err := parsePackageReferences(repo, filepath.Join(repo, "..", "escape.csproj")); err == nil {
		t.Fatalf("expected parsePackageReferences error for escaped path")
	}
	if _, err := parsePackageVersions(repo, filepath.Join(repo, "..", "escape.props")); err == nil {
		t.Fatalf("expected parsePackageVersions error for escaped path")
	}
	if _, _, err := readSourceFile(repo, filepath.Join(repo, "..", "escape.cs")); err == nil {
		t.Fatalf("expected readSourceFile error for escaped path")
	}
}

func TestWalkDirPermissionErrorBranches(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "missing-repo")
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), repo); err == nil {
		t.Fatalf("expected detect error for missing repo path")
	}
	if _, err := scanRepo(context.Background(), repo); err == nil {
		t.Fatalf("expected scan error for missing repo path")
	}
}

func assertResolverBranches(t *testing.T, mapper dependencyMapper) {
	t.Helper()
	if dep, ambiguous, undeclared := mapper.resolve("System.Text"); dep != "" || ambiguous || undeclared {
		t.Fatalf("expected system namespace to be ignored")
	}
	if dep, ambiguous, undeclared := mapper.resolve(""); dep != "" || ambiguous || undeclared {
		t.Fatalf("expected empty namespace to be ignored")
	}
	dep, ambiguous, undeclared := mapper.resolve("Acme.Foo")
	if dep != acmeBarName || !ambiguous || undeclared {
		t.Fatalf("expected ambiguous acme mapping, got dep=%q ambiguous=%v undeclared=%v", dep, ambiguous, undeclared)
	}
	dep, ambiguous, undeclared = mapper.resolve("Unknown.Vendor.Component")
	if dep != "unknown.vendor" || ambiguous || !undeclared {
		t.Fatalf("expected fallback undeclared mapping, got dep=%q ambiguous=%v undeclared=%v", dep, ambiguous, undeclared)
	}
}

func assertMatchScoreBranches(t *testing.T) {
	t.Helper()
	testCases := []struct {
		module     string
		dependency string
		want       int
	}{
		{newtonsoftName, newtonsoftName, 100},
		{"newtonsoft.json.serialization", newtonsoftName, 90},
		{"newtonsoft", newtonsoftName, 75},
		{"acme.foo", acmeBarName, 60},
		{"foo.client", "bar.client", 50},
		{"my.vendor.core", "vendor", 40},
		{"x.y", "a.b", 0},
	}
	for _, tc := range testCases {
		if got := matchScore(tc.module, tc.dependency); got != tc.want {
			t.Fatalf("matchScore mismatch for %q/%q: got %d want %d", tc.module, tc.dependency, got, tc.want)
		}
	}
}

func assertFallbackBranches(t *testing.T) {
	t.Helper()
	if got := fallbackDependency("acme.logging.core"); got != acmeLoggingName {
		t.Fatalf("fallbackDependency multi segment mismatch: %q", got)
	}
	if got := fallbackDependency("single"); got != "single" {
		t.Fatalf("fallbackDependency single segment mismatch: %q", got)
	}
	if got := firstSegment(""); got != "" {
		t.Fatalf("firstSegment empty mismatch: %q", got)
	}
}
