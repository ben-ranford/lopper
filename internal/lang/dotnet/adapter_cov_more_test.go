package dotnet

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

const dotNetProgramSource = "Program.cs"

func TestDotNetDetectWithConfidenceGuardBranches(t *testing.T) {
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("expected missing repo to fail detection")
	}

	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "app.sln"), 0o755); err != nil {
		t.Fatalf("mkdir solution dir: %v", err)
	}
	if applyRootSignals(repo, &language.Detection{}, map[string]struct{}{}) == nil {
		t.Fatalf("expected unreadable solution entry to fail root signal application")
	}

	repo = t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "obj"), 0o755); err != nil {
		t.Fatalf("mkdir obj dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, dotNetProgramSource), []byte("using System;\n"), 0o644); err != nil {
		t.Fatalf("write Program.cs: %v", err)
	}
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil || !detection.Matched {
		t.Fatalf("expected detection success with skipped obj dir, detection=%#v err=%v", detection, err)
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

	scanner := newRepoScanner(repo, newDependencyMapper(nil), &scanResult{
		AmbiguousByDependency:  map[string]int{},
		UndeclaredByDependency: map[string]int{},
	})
	if scanner.walk("", nil, context.Canceled) == nil {
		t.Fatalf("expected scanner walkErr to be returned")
	}
}

func TestDotNetScannerSkipsObjDirAndMissingSource(t *testing.T) {
	repo := t.TempDir()
	repoEntryPath := filepath.Join(repo, "obj")
	if err := os.Mkdir(repoEntryPath, 0o755); err != nil {
		t.Fatalf("mkdir obj dir: %v", err)
	}
	objEntry := mustReadDirEntry(t, repo, "obj")

	scanner := newRepoScanner(repo, newDependencyMapper(nil), &scanResult{
		AmbiguousByDependency:  map[string]int{},
		UndeclaredByDependency: map[string]int{},
	})
	if err := scanner.walk(repoEntryPath, objEntry, nil); err != filepath.SkipDir {
		t.Fatalf("expected scanner to skip obj dir, got %v", err)
	}

	filePath := filepath.Join(repo, dotNetProgramSource)
	if err := os.WriteFile(filePath, []byte("using Vendor.Pkg;\n"), 0o644); err != nil {
		t.Fatalf("write Program.cs: %v", err)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatalf("remove Program.cs: %v", err)
	}
	if scanner.scanFile(filePath) == nil {
		t.Fatalf("expected removed source file to fail scan")
	}
}

func TestDotNetAddMappingMetaAccumulates(t *testing.T) {
	scanner := newRepoScanner(t.TempDir(), newDependencyMapper(nil), &scanResult{
		AmbiguousByDependency:  map[string]int{},
		UndeclaredByDependency: map[string]int{},
	})
	scanner.addMappingMeta(mappingMetadata{
		ambiguousByDependency:  map[string]int{"dep": 1},
		undeclaredByDependency: map[string]int{"dep": 2},
	})
	if scanner.result.AmbiguousByDependency["dep"] != 1 || scanner.result.UndeclaredByDependency["dep"] != 2 {
		t.Fatalf("expected mapping metadata to accumulate, got %#v %#v", scanner.result.AmbiguousByDependency, scanner.result.UndeclaredByDependency)
	}
}

func TestDotNetCollectorSkipsObjDir(t *testing.T) {
	repo := t.TempDir()
	repoEntryPath := filepath.Join(repo, "obj")
	if err := os.Mkdir(repoEntryPath, 0o755); err != nil {
		t.Fatalf("mkdir obj dir: %v", err)
	}
	objEntry := mustReadDirEntry(t, repo, "obj")
	if err := newDependencyCollector(repo, map[string]struct{}{}).walk(repoEntryPath, objEntry, nil); err != filepath.SkipDir {
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
	if deps, err := parseManifestDependenciesForEntry(t.TempDir(), filepath.Join(t.TempDir(), "README.md"), "README.md"); err != nil || len(deps) != 0 {
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
