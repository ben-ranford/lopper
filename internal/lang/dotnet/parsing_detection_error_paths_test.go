package dotnet

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	appProjectName      = "App.csproj"
	programSourceName   = "Program.cs"
	newtonsoftName      = "newtonsoft.json"
	acmeBarName         = "acme.bar"
	acmeLoggingName     = "acme.logging"
	acmeFooSourceImport = "using Acme.Foo;\n"
	fooBarImportLine    = "using Foo.Bar;"
)

func TestDetectAndRootSignalBranches(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, appProjectName), `<Project Sdk="Microsoft.NET.Sdk"></Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "App.sln"), "Project(\"{FAKE}\") = \"App\", \"src\\\\App\\\\"+appProjectName+"\", \"{ONE}\"\nEndProject\n")
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

func TestCollectDeclaredDependenciesIgnoresAncestorCentralPackages(t *testing.T) {
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
	if slices.Contains(deps, "acme.platform") {
		t.Fatalf("expected ancestor central package manifest to be ignored, got %#v", deps)
	}
	if !slices.Contains(deps, "dapper") {
		t.Fatalf("expected local dependency to remain visible, got %#v", deps)
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

func TestByteCSharpAndFSharpParsingBranches(t *testing.T) {
	if mod, alias, ok := parseCSharpUsingBytes([]byte(" " + fooBarImportLine + " ")); !ok || mod != "Foo.Bar" || alias != "" {
		t.Fatalf("expected byte csharp using parse, got mod=%q alias=%q ok=%v", mod, alias, ok)
	}
	if _, _, ok := parseCSharpUsingBytes([]byte("using Foo.Bar")); ok {
		t.Fatalf("expected missing semicolon to fail")
	}
	if _, _, ok := parseCSharpUsingBytes([]byte("using Alias = ;")); ok {
		t.Fatalf("expected empty alias target to fail")
	}
	if _, ok := parseFSharpOpenBytes([]byte("open 1Invalid.Namespace")); ok {
		t.Fatalf("expected invalid fsharp namespace to fail")
	}
}

func TestByteWhitespaceAndCommentHelpers(t *testing.T) {
	if next, ok := consumeKeyword([]byte(fooBarImportLine), "using"); !ok || string(next) != "Foo.Bar;" {
		t.Fatalf("consumeKeyword mismatch: next=%q ok=%v", next, ok)
	}
	if _, ok := consumeKeyword([]byte("using"), "using"); ok {
		t.Fatalf("expected consumeKeyword to require trailing whitespace")
	}
	if !hasBytesPrefix([]byte("using Foo"), "using") || hasBytesPrefix([]byte("use Foo"), "using") {
		t.Fatalf("unexpected hasBytesPrefix behavior")
	}
	if !isSpaceByte('\n') || isSpaceByte('x') {
		t.Fatalf("unexpected isSpaceByte behavior")
	}
	if !bytes.Equal(stripLineCommentBytes([]byte(" "+fooBarImportLine+" // note ")), []byte(fooBarImportLine)) {
		t.Fatalf("stripLineCommentBytes failed to trim comment")
	}
	if !bytes.Equal(stripLineCommentBytes([]byte(" "+fooBarImportLine+" ")), []byte(fooBarImportLine)) {
		t.Fatalf("stripLineCommentBytes failed to trim whitespace")
	}
	if got := string(trimTrailingCarriageReturn([]byte("value\r"))); got != "value" {
		t.Fatalf("trimTrailingCarriageReturn mismatch: %q", got)
	}
	if got := string(trimTrailingCarriageReturn([]byte("value"))); got != "value" {
		t.Fatalf("trimTrailingCarriageReturn without carriage return mismatch: %q", got)
	}
	if got := firstContentColumnBytes([]byte("\t  value")); got != 4 {
		t.Fatalf("firstContentColumnBytes mismatch: %d", got)
	}
	if got := firstContentColumnBytes([]byte("   ")); got != 1 {
		t.Fatalf("firstContentColumnBytes blank mismatch: %d", got)
	}
}

func TestStripSourceCommentsBytesFastPathAllocations(t *testing.T) {
	line := []byte("  using Foo.Bar;  ")
	if allocs := testing.AllocsPerRun(1000, func() {
		got, column, inBlock := stripSourceCommentsBytes(line, false)
		if inBlock {
			t.Fatalf("expected fast path to keep block comment state false")
		}
		if column != 3 {
			t.Fatalf("expected first non-space column 3, got %d", column)
		}
		if !bytes.Equal(got, []byte("using Foo.Bar;")) {
			t.Fatalf("unexpected stripped line: %q", got)
		}
	}); allocs != 0 {
		t.Fatalf("expected zero allocations on no-comment fast path, got %f", allocs)
	}
}

func TestStripSourceCommentsBytesAdditionalBranches(t *testing.T) {
	if got, column, inBlock := stripSourceCommentsBytes(nil, false); len(got) != 0 || column != 1 || inBlock {
		t.Fatalf("expected nil input to remain empty, got %q column=%d inBlock=%v", got, column, inBlock)
	}

	if got, column, inBlock := stripSourceCommentsBytes([]byte("// comment only"), false); len(got) != 0 || column != 1 || inBlock {
		t.Fatalf("expected line comment to strip to empty, got %q column=%d inBlock=%v", got, column, inBlock)
	}

	got, column, inBlock := stripSourceCommentsBytes([]byte("/* comment */ using Foo.Bar;"), false)
	if !bytes.Equal(got, []byte("using Foo.Bar;")) || column <= 1 || inBlock {
		t.Fatalf("expected closed block comment prefix to preserve trailing import, got %q column=%d inBlock=%v", got, column, inBlock)
	}

	got, column, inBlock = stripSourceCommentsBytes([]byte("using /* comment */ Foo.Bar;"), false)
	if !bytes.Contains(got, []byte("using")) || !bytes.Contains(got, []byte("Foo.Bar;")) || column != 1 || inBlock {
		t.Fatalf("expected inline block comment to preserve import fragments, got %q column=%d inBlock=%v", got, column, inBlock)
	}

	if got, column, inBlock := stripSourceCommentsBytes([]byte("/* unterminated"), false); len(got) != 0 || column != 1 || !inBlock {
		t.Fatalf("expected unterminated block comment to carry state forward, got %q column=%d inBlock=%v", got, column, inBlock)
	}

	got, column, inBlock = stripSourceCommentsBytes([]byte("still hidden */ using Foo.Bar;"), true)
	if !bytes.Equal(got, []byte("using Foo.Bar;")) || column <= 1 || inBlock {
		t.Fatalf("expected closing block comment to resume parsing, got %q column=%d inBlock=%v", got, column, inBlock)
	}
}

func TestAdvanceSourceCommentStateBranches(t *testing.T) {
	if next, column, inBlock, stop, handled := advanceSourceCommentState([]byte("x"), 0, 1, false); next != 0 || column != 1 || inBlock || stop || handled {
		t.Fatalf("expected single-byte non-comment to be ignored, got next=%d column=%d inBlock=%v stop=%v handled=%v", next, column, inBlock, stop, handled)
	}

	if next, column, inBlock, stop, handled := advanceSourceCommentState([]byte("// comment"), 0, 1, false); next != 0 || column != 1 || inBlock || !stop || !handled {
		t.Fatalf("expected line comment to stop parsing, got next=%d column=%d inBlock=%v stop=%v handled=%v", next, column, inBlock, stop, handled)
	}

	if next, column, inBlock, stop, handled := advanceSourceCommentState([]byte("/* comment"), 0, 1, false); next != 2 || column != 3 || !inBlock || stop || !handled {
		t.Fatalf("expected block comment opener to advance state, got next=%d column=%d inBlock=%v stop=%v handled=%v", next, column, inBlock, stop, handled)
	}

	if next, column, inBlock, stop, handled := advanceSourceCommentState([]byte("*/"), 0, 1, true); next != 2 || column != 3 || inBlock || stop || !handled {
		t.Fatalf("expected block comment closer to clear state, got next=%d column=%d inBlock=%v stop=%v handled=%v", next, column, inBlock, stop, handled)
	}

	if next, column, inBlock, stop, handled := advanceSourceCommentState([]byte("x"), 0, 1, true); next != 1 || column != 2 || !inBlock || stop || !handled {
		t.Fatalf("expected in-block content to advance one byte, got next=%d column=%d inBlock=%v stop=%v handled=%v", next, column, inBlock, stop, handled)
	}
}

func TestIsRepoBoundedPathAdditionalBranches(t *testing.T) {
	repo := t.TempDir()
	if !isRepoBoundedPath(repo, repo) {
		t.Fatalf("expected repo root itself to be repo-bounded")
	}
	if !isRepoBoundedPath(repo, filepath.Join(repo, ".", "src", "..", "src")) {
		t.Fatalf("expected cleaned in-repo candidate to remain repo-bounded")
	}
	if isRepoBoundedPath(repo, filepath.Join(filepath.Dir(repo), filepath.Base(repo)+"-sibling")) {
		t.Fatalf("expected sibling path not to be repo-bounded")
	}
}

func TestAnalyseDeclaredDependenciesWithoutTarget(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, appProjectName), `<Project><ItemGroup><PackageReference Include="Newtonsoft.Json" Version="13.0.3" /></ItemGroup></Project>`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo})
	if err != nil {
		t.Fatalf("analyse declared dependency without target: %v", err)
	}
	if !slices.Contains(reportData.Warnings, "no dependency or top-N target provided") {
		t.Fatalf("expected missing target warning, got %#v", reportData.Warnings)
	}
	if slices.Contains(reportData.Warnings, "no .NET package dependencies discovered from project manifests") {
		t.Fatalf("did not expect missing manifest warning when dependency is declared, got %#v", reportData.Warnings)
	}
}

func TestByteDependencyHelperBranches(t *testing.T) {
	mapper := newDependencyMapper([]string{"Acme.Core", "", "serilog.aspnetcore"})
	if len(mapper.declared) != 2 || mapper.declared[0].id != "acme.core" {
		t.Fatalf("unexpected mapper normalization: %#v", mapper.declared)
	}
	if got := fallbackDependencyID(""); got != "" {
		t.Fatalf("fallbackDependencyID empty mismatch: %q", got)
	}
	if got := fallbackDependencyID("one.two"); got != "one.two" {
		t.Fatalf("fallbackDependencyID two segment mismatch: %q", got)
	}
	if binding := parseFSharpImportLine([]byte("open System"), programSourceName, 1, 1, mapper, &mappingMetadata{}); binding != nil {
		t.Fatalf("expected unresolved system open to be ignored, got %#v", binding)
	}
}

func TestParseImportsWithCRLFPreservesColumns(t *testing.T) {
	imports, _ := parseImports([]byte("\t"+fooBarImportLine+"\r\n// comment only\r\n"), programSourceName, newDependencyMapper([]string{"foo.bar"}))
	if len(imports) != 1 || imports[0].Location.Column != 2 {
		t.Fatalf("expected CRLF import parse with preserved column, got %#v", imports)
	}
}

func TestParseImportsIgnoreBlockComments(t *testing.T) {
	mapper := newDependencyMapper([]string{"foo.bar"})
	imports, _ := parseImports([]byte("/*\nusing Foo.Bar;\n*/\n"+fooBarImportLine+"\n"), programSourceName, mapper)
	if len(imports) != 1 {
		t.Fatalf("expected only one import outside block comments, got %#v", imports)
	}
	if imports[0].Location.Line != 4 {
		t.Fatalf("expected import line 4 after skipping block comment, got %#v", imports[0].Location)
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
	if len(imports) < 3 {
		t.Fatalf("expected imports parsed, got %#v", imports)
	}
	if meta.ambiguousByDependency[acmeBarName] == 0 {
		t.Fatalf("expected ambiguous mapping count for acme.bar")
	}
	assertImportBindings(t, imports)
	assertEmptyDependencySelectionWarnings(t)
	assertRiskyDependencyReport(t)
}

func assertImportBindings(t *testing.T, imports []importBinding) {
	t.Helper()
	aliasFound := false
	wildcardCount := 0
	for _, imported := range imports {
		switch {
		case imported.Name == "Logger":
			aliasFound = true
			if imported.Wildcard || imported.Local != "Logger" {
				t.Fatalf("expected alias import to keep local identifier, got %#v", imported)
			}
		case imported.Name == "*" && imported.Module == "Acme.Foo":
			wildcardCount++
			if !imported.Wildcard || imported.Local != "" {
				t.Fatalf("expected namespace/open import to be wildcard with empty local, got %#v", imported)
			}
		}
	}
	if !aliasFound {
		t.Fatalf("expected alias import binding")
	}
	if wildcardCount < 2 {
		t.Fatalf("expected wildcard bindings for csharp using + fsharp open, got %d (%#v)", wildcardCount, imports)
	}
}

func assertEmptyDependencySelectionWarnings(t *testing.T) {
	t.Helper()
	empty := scanResult{
		AmbiguousByDependency:  map[string]int{},
		UndeclaredByDependency: map[string]int{},
	}
	deps, warnings := buildRequestedDotNetDependencies(language.Request{}, empty)
	if len(deps) != 0 || len(warnings) == 0 {
		t.Fatalf("expected missing target warning for buildRequestedDotNetDependencies")
	}
	deps, warnings = buildTopDotNetDependencies(5, empty, 40, report.DefaultRemovalCandidateWeights())
	if len(deps) != 0 || len(warnings) == 0 {
		t.Fatalf("expected empty top-N warning")
	}
}

func assertRiskyDependencyReport(t *testing.T) {
	t.Helper()
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
	repo := t.TempDir()
	sln := filepath.Join(repo, "App.sln")
	testutil.MustWriteFile(t, sln, `
Project("{FAKE}") = "App", "src\\App\\App.csproj", "{ONE}"
Project("{FAKE}") = "Lib", "src\\Lib\\Lib.fsproj", "{TWO}"
Project("{FAKE}") = "Escaped", "..\\outside\\Outside.csproj", "{THREE}"
EndProject
`)
	roots := map[string]struct{}{}
	if err := addSolutionRoots(repo, sln, roots); err != nil {
		t.Fatalf("addSolutionRoots: %v", err)
	}
	if len(roots) < 2 {
		t.Fatalf("expected multiple roots from solution, got %#v", roots)
	}
	outsideRoot := filepath.Join(filepath.Dir(repo), "outside")
	if _, ok := roots[outsideRoot]; ok {
		t.Fatalf("expected escaped solution project root to be ignored, got %#v", roots)
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

	noManifestPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(noManifestPath, programSourceName), "using Missing.Package;\n")
	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: noManifestPath,
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
