package dotnet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const dotNetProgramSource = "Program.cs"
const dotNetReadmeFile = "README.md"
const dotNetMkdirObjDirErrFmt = "mkdir obj dir: %v"
const dotNetWriteProgramFileErrFmt = "write " + dotNetProgramSource + ": %v"

func TestDotNetDetectWithConfidenceGuardBranches(t *testing.T) {
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("expected missing repo to fail detection")
	}

	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "app.sln"), 0o755); err != nil {
		t.Fatalf("mkdir solution dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, "App.csproj"), 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	detection := language.Detection{}
	roots := map[string]struct{}{}
	if err := applyRootSignals(repo, &detection, roots); err != nil {
		t.Fatalf("expected directory-shaped manifest entries to be ignored, got %v", err)
	}
	if detection.Matched || detection.Confidence != 0 || len(roots) != 0 {
		t.Fatalf("expected directory-shaped manifest entries to contribute no root signals, got detection=%#v roots=%#v", detection, roots)
	}
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with only directory-shaped manifests: %v", err)
	}
	if detection.Matched || detection.Confidence != 0 || len(detection.Roots) != 0 {
		t.Fatalf("expected directory-shaped manifests to be ignored by detection, got %#v", detection)
	}

	repo = t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "obj"), 0o755); err != nil {
		t.Fatalf(dotNetMkdirObjDirErrFmt, err)
	}
	if err := os.WriteFile(filepath.Join(repo, dotNetProgramSource), []byte("using System;\n"), 0o644); err != nil {
		t.Fatalf(dotNetWriteProgramFileErrFmt, err)
	}
	detection, err = NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil || !detection.Matched {
		t.Fatalf("expected detection success with skipped obj dir, detection=%#v err=%v", detection, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewAdapter().DetectWithConfidence(ctx, repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled detection, got %v", err)
	}
}

func TestDotNetAnalyseEmptyRepoWarnsNoDependencies(t *testing.T) {
	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: t.TempDir(), TopN: 1})
	if err != nil {
		t.Fatalf("analyse empty repo: %v", err)
	}
	if !slices.Contains(result.Warnings, "no .NET package dependencies discovered from project manifests") {
		t.Fatalf("expected no dependencies warning, got %#v", result.Warnings)
	}
}

func TestDotNetMarkDetectionZeroConfidence(t *testing.T) {
	detection := language.Detection{}
	roots := map[string]struct{}{}
	markDetection(&detection, roots, 0, "repo")
	if detection.Matched || detection.Confidence != 0 || len(roots) != 0 {
		t.Fatalf("expected zero-confidence detection to remain unchanged, got %#v %#v", detection, roots)
	}
}

func TestDotNetResolveRemovalCandidateWeightsNormalizes(t *testing.T) {
	normalized := resolveRemovalCandidateWeights(&report.RemovalCandidateWeights{Usage: 2, Impact: 1, Confidence: 1})
	if normalized.Usage <= 0 || normalized.Impact <= 0 || normalized.Confidence <= 0 {
		t.Fatalf("expected normalized weights, got %#v", normalized)
	}
}

func TestDotNetAddAncestorCentralPackagesParseError(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "src", "service")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	if err := os.Mkdir(filepath.Join(parent, centralPackagesFile), 0o755); err != nil {
		t.Fatalf("mkdir central packages dir: %v", err)
	}
	if addAncestorCentralPackages(repo, map[string]struct{}{}) == nil {
		t.Fatalf("expected ancestor central package parse error")
	}
}

func TestDotNetCaptureMatchesIgnoresMalformedInput(t *testing.T) {
	if !slices.Equal(captureMatches([][][]byte{{[]byte("skip")}, {[]byte(" "), []byte(" ")}}), []string{}) {
		t.Fatalf("expected malformed/blank captureMatches input to be ignored")
	}
}

func TestDotNetCollectorAndScannerReturnWalkErrors(t *testing.T) {
	repo := t.TempDir()
	collector := newDependencyCollector(repo, map[string]struct{}{})
	if collector.walk("", nil, context.Canceled) == nil {
		t.Fatalf("expected collector walkErr to be returned")
	}

	discoverer := newSourceDiscoverer(repo, &sourceDiscovery{})
	if discoverer.walk("", nil, context.Canceled) == nil {
		t.Fatalf("expected source discoverer walkErr to be returned")
	}
}

func TestDotNetScannerSkipsObjDirAndMissingSource(t *testing.T) {
	repo := t.TempDir()
	repoEntryPath := filepath.Join(repo, "obj")
	if err := os.Mkdir(repoEntryPath, 0o755); err != nil {
		t.Fatalf(dotNetMkdirObjDirErrFmt, err)
	}
	objEntry := mustReadDirEntry(t, repo, "obj")

	discoverer := newSourceDiscoverer(repo, &sourceDiscovery{})
	if err := discoverer.walk(repoEntryPath, objEntry, nil); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected source discoverer to skip obj dir, got %v", err)
	}

	filePath := filepath.Join(repo, dotNetProgramSource)
	if err := os.WriteFile(filePath, []byte("using Vendor.Pkg;\n"), 0o644); err != nil {
		t.Fatalf(dotNetWriteProgramFileErrFmt, err)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatalf("remove Program.cs: %v", err)
	}
	if discoverer.discoverFile(filePath) == nil {
		t.Fatalf("expected removed source file to fail discovery")
	}
}

func TestDotNetSourceDiscovererWalkCoversDirectoryAndFile(t *testing.T) {
	repo := t.TempDir()
	srcDir := filepath.Join(repo, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src dir: %v", err)
	}
	dirEntry := mustReadDirEntry(t, repo, "src")

	discovery := sourceDiscovery{}
	discoverer := newSourceDiscoverer(repo, &discovery)
	if err := discoverer.walk(srcDir, dirEntry, nil); err != nil {
		t.Fatalf("walk src dir: %v", err)
	}

	sourcePath := filepath.Join(srcDir, dotNetProgramSource)
	if err := os.WriteFile(sourcePath, []byte("using Vendor.Pkg;\n"), 0o644); err != nil {
		t.Fatalf(dotNetWriteProgramFileErrFmt, err)
	}
	sourceEntry := mustReadDirEntry(t, srcDir, dotNetProgramSource)
	if err := discoverer.walk(sourcePath, sourceEntry, nil); err != nil {
		t.Fatalf("walk source file: %v", err)
	}
	if len(discovery.Files) != 1 || discovery.Files[0].RelativePath != filepath.Join("src", dotNetProgramSource) {
		t.Fatalf("unexpected source discovery: %#v", discovery.Files)
	}
}

func TestDotNetScanInputDiscovererBranches(t *testing.T) {
	repo := t.TempDir()
	objDir := filepath.Join(repo, "obj")
	if err := os.Mkdir(objDir, 0o755); err != nil {
		t.Fatalf(dotNetMkdirObjDirErrFmt, err)
	}
	objEntry := mustReadDirEntry(t, repo, "obj")

	discovery := sourceDiscovery{}
	discoverer := newScanInputDiscoverer(repo, &discovery)
	if err := discoverer.walk(objDir, objEntry, nil); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected scan input discoverer to skip obj dir, got %v", err)
	}

	manifestPath := filepath.Join(repo, "App.csproj")
	testutil.MustWriteFile(t, manifestPath, `<Project><ItemGroup><PackageReference Include="Vendor.Pkg" /></ItemGroup></Project>`)
	manifestEntry := mustReadDirEntry(t, repo, "App.csproj")
	if err := discoverer.walk(manifestPath, manifestEntry, nil); err != nil {
		t.Fatalf("walk manifest: %v", err)
	}
	if _, ok := discoverer.dependencySet["vendor.pkg"]; !ok {
		t.Fatalf("expected manifest dependency, got %#v", discoverer.dependencySet)
	}

	badManifestPath := filepath.Join(repo, "Bad.csproj")
	testutil.MustWriteFile(t, badManifestPath, `<Project />`)
	badManifestEntry := mustReadDirEntry(t, repo, "Bad.csproj")
	if err := os.Remove(badManifestPath); err != nil {
		t.Fatalf("remove bad manifest: %v", err)
	}
	if err := discoverer.walk(badManifestPath, badManifestEntry, nil); err == nil {
		t.Fatalf("expected removed manifest to fail scan input discovery")
	}

	discoverer.sourceScanLimited = true
	sourcePath := filepath.Join(repo, dotNetProgramSource)
	testutil.MustWriteFile(t, sourcePath, "using Vendor.Pkg;\n")
	sourceEntry := mustReadDirEntry(t, repo, dotNetProgramSource)
	if err := discoverer.walk(sourcePath, sourceEntry, nil); err != nil {
		t.Fatalf("walk source while limited: %v", err)
	}
	if len(discovery.Files) != 0 {
		t.Fatalf("expected limited source scan to skip new source files, got %#v", discovery.Files)
	}
}

func TestDotNetAddMappingMetaAccumulates(t *testing.T) {
	result := scanResult{
		AmbiguousByDependency:  map[string]int{},
		UndeclaredByDependency: map[string]int{},
	}
	addMappingMeta(&result, mappingMetadata{
		ambiguousByDependency:  map[string]int{"dep": 1},
		undeclaredByDependency: map[string]int{"dep": 2},
	})
	if result.AmbiguousByDependency["dep"] != 1 || result.UndeclaredByDependency["dep"] != 2 {
		t.Fatalf("expected mapping metadata to accumulate, got %#v %#v", result.AmbiguousByDependency, result.UndeclaredByDependency)
	}
}

func TestDotNetCollectorSkipsObjDir(t *testing.T) {
	repo := t.TempDir()
	repoEntryPath := filepath.Join(repo, "obj")
	if err := os.Mkdir(repoEntryPath, 0o755); err != nil {
		t.Fatalf(dotNetMkdirObjDirErrFmt, err)
	}
	objEntry := mustReadDirEntry(t, repo, "obj")
	if err := newDependencyCollector(repo, map[string]struct{}{}).walk(repoEntryPath, objEntry, nil); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected collector to skip obj dir, got %v", err)
	}
}

func TestDotNetCollectorFailsRemovedManifest(t *testing.T) {
	repo := t.TempDir()
	manifestPath := filepath.Join(repo, "Bad.csproj")
	if err := os.WriteFile(manifestPath, []byte(`<Project />`), 0o644); err != nil {
		t.Fatalf("write manifest file: %v", err)
	}
	manifestEntry := mustReadDirEntry(t, repo, "Bad.csproj")
	if err := os.Remove(manifestPath); err != nil {
		t.Fatalf("remove manifest file: %v", err)
	}
	if newDependencyCollector(repo, map[string]struct{}{}).walk(manifestPath, manifestEntry, nil) == nil {
		t.Fatalf("expected manifest parse to fail for removed file")
	}
}

func TestDotNetParseCSharpImportLineFallsBackToUndeclaredBinding(t *testing.T) {
	mapper := newDependencyMapper(nil)
	meta := &mappingMetadata{
		ambiguousByDependency:  map[string]int{},
		undeclaredByDependency: map[string]int{},
	}
	binding, handled := parseCSharpImportLine([]byte("using Mystery.Component;"), dotNetProgramSource, 1, 1, mapper, meta)
	if !handled || binding == nil || binding.Dependency != "mystery.component" {
		t.Fatalf("expected fallback undeclared binding, got handled=%v binding=%#v", handled, binding)
	}
	if meta.undeclaredByDependency["mystery.component"] != 1 {
		t.Fatalf("expected undeclared dependency metadata, got %#v", meta.undeclaredByDependency)
	}
}

func TestDotNetImportResolutionHelpers(t *testing.T) {
	mapper := newDependencyMapper(nil)
	meta := &mappingMetadata{
		ambiguousByDependency:  map[string]int{},
		undeclaredByDependency: map[string]int{},
	}
	if binding := parseFSharpImportLine([]byte("open System"), "Program.fs", 1, 1, mapper, meta); binding != nil {
		t.Fatalf("expected framework namespace to be ignored, got %#v", binding)
	}
	if dependency, resolved := resolveImportDependency("System.Text", mapper, meta); resolved || dependency != "" {
		t.Fatalf("expected system import to be ignored, got dependency=%q resolved=%v", dependency, resolved)
	}
	if deps, err := parseManifestDependenciesForEntry(t.TempDir(), filepath.Join(t.TempDir(), dotNetReadmeFile), dotNetReadmeFile); err != nil || len(deps) != 0 {
		t.Fatalf("expected non-manifest entry to be ignored, got deps=%#v err=%v", deps, err)
	}
	if module, alias, ok := parseCSharpUsing("using ;"); ok || module != "" || alias != "" {
		t.Fatalf("expected blank using expression to fail parse, module=%q alias=%q ok=%v", module, alias, ok)
	}
}

func TestDotNetBuildTopDependenciesAndHelperGuards(t *testing.T) {
	reports, warnings := buildTopDotNetDependencies(1, scanResult{DeclaredDependencies: []string{"alpha", "beta"}}, 50, report.DefaultRemovalCandidateWeights())
	if len(reports) != 1 || len(warnings) != 0 {
		t.Fatalf("expected top-N slicing without extra warnings, reports=%#v warnings=%#v", reports, warnings)
	}

	mapperWithScores := newDependencyMapper([]string{"vendor", "vendor.pkg"})
	dependency, ambiguous, undeclared := mapperWithScores.resolve("Vendor.Client")
	if dependency != "vendor" || ambiguous || undeclared {
		t.Fatalf("expected best-score dependency match, got dependency=%q ambiguous=%v undeclared=%v", dependency, ambiguous, undeclared)
	}
	if shouldSkipDir("src") {
		t.Fatalf("did not expect src to be skipped")
	}
	if isSourceFile("notes.txt") {
		t.Fatalf("did not expect txt file to count as source")
	}
	if isRepoBoundedPath("\x00", ".") {
		t.Fatalf("expected invalid repo path to be rejected")
	}
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00", TopN: 1}); err == nil {
		t.Fatalf("expected analyse to fail for invalid repo path")
	}
}

func TestDotNetSignalForNameDefaultBranch(t *testing.T) {
	if signalForName(dotNetReadmeFile) != fileSignalNone {
		t.Fatalf("expected non-.NET filename to produce no detection signal")
	}
}

func TestDotNetAdditionalZeroHitBranches(t *testing.T) {
	t.Run("ancestor central packages success and no-op", func(t *testing.T) {
		parent := t.TempDir()
		repo := filepath.Join(parent, "src", "service")
		if err := os.MkdirAll(repo, 0o755); err != nil {
			t.Fatalf("mkdir repo: %v", err)
		}
		centralPackagesXML := `
<Project>
  <ItemGroup>
    <PackageVersion Include="Serilog.AspNetCore" Version="8.0.0" />
  </ItemGroup>
</Project>`
		if err := os.WriteFile(filepath.Join(parent, centralPackagesFile), []byte(centralPackagesXML), 0o644); err != nil {
			t.Fatalf("write central packages file: %v", err)
		}

		set := map[string]struct{}{}
		if err := addAncestorCentralPackages(repo, set); err != nil {
			t.Fatalf("add ancestor central packages: %v", err)
		}
		if _, ok := set["serilog.aspnetcore"]; !ok {
			t.Fatalf("expected central package to be added, got %#v", set)
		}

		set = map[string]struct{}{}
		if err := addAncestorCentralPackages(t.TempDir(), set); err != nil {
			t.Fatalf("expected missing ancestor central packages to return nil, got %v", err)
		}
		if len(set) != 0 {
			t.Fatalf("expected no dependencies when no ancestor file exists, got %#v", set)
		}
	})

	t.Run("mapper resolves ambiguous ties deterministically", func(t *testing.T) {
		mapper := newDependencyMapper([]string{"acme.foo", "acme.bar"})
		dependency, ambiguous, undeclared := mapper.resolve("Acme.Baz")
		if dependency != "acme.bar" || !ambiguous || undeclared {
			t.Fatalf("expected lexicographically smaller dependency on tie, got dependency=%q ambiguous=%v undeclared=%v", dependency, ambiguous, undeclared)
		}
	})
}

func TestDotNetDiscoveryAndParsingStagesCompose(t *testing.T) {
	repo := t.TempDir()
	manifest := []byte(`
<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Newtonsoft.Json" Version="13.0.3" />
  </ItemGroup>
</Project>`)
	if err := os.WriteFile(filepath.Join(repo, "App.csproj"), manifest, 0o644); err != nil {
		t.Fatalf("write App.csproj: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, dotNetProgramSource), []byte("using Newtonsoft.Json;\n"), 0o644); err != nil {
		t.Fatalf(dotNetWriteProgramFileErrFmt, err)
	}

	inputs, err := discoverScanInputs(context.Background(), repo)
	if err != nil {
		t.Fatalf("discover scan inputs: %v", err)
	}
	if !slices.Equal(inputs.DeclaredDependencies, []string{"newtonsoft.json"}) {
		t.Fatalf("unexpected declared dependencies: %#v", inputs.DeclaredDependencies)
	}
	if len(inputs.SourceFiles) != 1 || inputs.SourceFiles[0].RelativePath != dotNetProgramSource {
		t.Fatalf("unexpected discovered source files: %#v", inputs.SourceFiles)
	}

	parsed := parseSourceDocument(inputs.SourceFiles[0], newDependencyMapper(inputs.DeclaredDependencies))
	if len(parsed.File.Imports) != 1 || parsed.File.Imports[0].Dependency != "newtonsoft.json" {
		t.Fatalf("unexpected parsed imports: %#v", parsed.File.Imports)
	}
	if parsed.File.Path != dotNetProgramSource {
		t.Fatalf("unexpected parsed file path: %#v", parsed.File)
	}
}

func TestDotNetDiscoverScanInputsPreservesMixedRepoOutputs(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, centralPackagesFile), `
<Project>
  <ItemGroup>
    <PackageVersion Include="Acme.Logging" Version="1.2.3" />
  </ItemGroup>
</Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "App", "App.csproj"), `
<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Newtonsoft.Json" Version="13.0.3" />
  </ItemGroup>
</Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "Lib", "Lib.fsproj"), `
<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="FSharp.Core" Version="8.0.0" />
  </ItemGroup>
</Project>`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "App", "packages.lock.json"), `{"version":1}`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "App", "Program.cs"), "using Newtonsoft.Json;\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "Lib", "Module.fs"), "open Acme.Logging\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "App", "Generated.g.cs"), "using Generated;\n")

	inputs, err := discoverScanInputs(context.Background(), repo)
	if err != nil {
		t.Fatalf("discover scan inputs: %v", err)
	}

	if !slices.Equal(inputs.DeclaredDependencies, []string{"acme.logging", "fsharp.core", "newtonsoft.json"}) {
		t.Fatalf("unexpected declared dependencies: %#v", inputs.DeclaredDependencies)
	}
	if inputs.SkippedGenerated != 1 {
		t.Fatalf("expected one generated source file skip, got %d", inputs.SkippedGenerated)
	}
	if !slices.Contains(inputs.Warnings, "skipped 1 generated source file(s)") {
		t.Fatalf("expected generated source warning, got %#v", inputs.Warnings)
	}
	if len(inputs.SourceFiles) != 2 {
		t.Fatalf("expected two source files, got %#v", inputs.SourceFiles)
	}

	paths := make([]string, 0, len(inputs.SourceFiles))
	for _, file := range inputs.SourceFiles {
		paths = append(paths, file.RelativePath)
	}
	if !slices.Contains(paths, filepath.Join("src", "App", "Program.cs")) {
		t.Fatalf("expected Program.cs source document, got %#v", paths)
	}
	if !slices.Contains(paths, filepath.Join("src", "Lib", "Module.fs")) {
		t.Fatalf("expected Module.fs source document, got %#v", paths)
	}
}

func TestDotNetDiscoverScanInputsKeepsManifestDiscoveryAfterSourceCap(t *testing.T) {
	repo := t.TempDir()
	for i := 0; i < maxScanFiles+1; i++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "a-src", "f"+strconv.Itoa(i)+".cs"), "using Acme.Foo;\n")
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "z-manifests", "Late.csproj"), `
<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Late.Manifest" Version="1.0.0" />
  </ItemGroup>
</Project>`)

	inputs, err := discoverScanInputs(context.Background(), repo)
	if err != nil {
		t.Fatalf("discover scan inputs with cap: %v", err)
	}

	if !inputs.SkippedFileLimit {
		t.Fatalf("expected source scan cap to be triggered")
	}
	if len(inputs.SourceFiles) != maxScanFiles {
		t.Fatalf("expected capped source file count %d, got %d", maxScanFiles, len(inputs.SourceFiles))
	}
	if !slices.Contains(inputs.DeclaredDependencies, "late.manifest") {
		t.Fatalf("expected dependencies discovered after source cap, got %#v", inputs.DeclaredDependencies)
	}
	expectedWarning := fmt.Sprintf("source scan capped at %d files", maxScanFiles)
	if !slices.Contains(inputs.Warnings, expectedWarning) {
		t.Fatalf("expected source cap warning, got %#v", inputs.Warnings)
	}
}

func TestDotNetSolutionRootsRemainRepoBounded(t *testing.T) {
	repo := t.TempDir()
	projectPath := filepath.Join(repo, "src", "App", "App.csproj")
	testutil.MustWriteFile(t, projectPath, `<Project />`)
	solutionPath := filepath.Join(repo, "App.sln")
	testutil.MustWriteFile(t, solutionPath, `
Project("{FAE04EC0-301F-11D3-BF4B-00C04F79EFBC}") = "App", "src/App/App.csproj", "{11111111-1111-1111-1111-111111111111}"
EndProject
Project("{FAE04EC0-301F-11D3-BF4B-00C04F79EFBC}") = "Outside", "../outside/Outside.csproj", "{22222222-2222-2222-2222-222222222222}"
EndProject
`)

	roots := map[string]struct{}{}
	if err := addSolutionRoots(repo, solutionPath, roots); err != nil {
		t.Fatalf("add solution roots: %v", err)
	}
	if _, ok := roots[filepath.Dir(projectPath)]; !ok {
		t.Fatalf("expected in-repo project root, got %#v", roots)
	}
	if len(roots) != 1 {
		t.Fatalf("expected only in-repo project root, got %#v", roots)
	}
	if !isRepoBoundedPath(repo, filepath.Join(repo, "src")) {
		t.Fatalf("expected src to be repo-bounded")
	}
	if isRepoBoundedPath(repo, filepath.Dir(repo)) {
		t.Fatalf("did not expect parent dir to be repo-bounded")
	}
	if isRepoBoundedPath(repo, "\x00") {
		t.Fatalf("expected invalid candidate path to be rejected")
	}
	if err := addSolutionRoots(repo, filepath.Join(repo, "missing.sln"), map[string]struct{}{}); err == nil {
		t.Fatalf("expected missing solution to fail")
	}
}

func mustReadDirEntry(t *testing.T, dir, name string) os.DirEntry {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return entry
		}
	}
	t.Fatalf("expected %s dir entry", name)
	return nil
}
