package js

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	codemodMissingSource = "missing.js"
	codemodIndexSource   = "index.js"
)

func TestBuildCodemodForMissingSourceWarnsAndSkipsSuggestions(t *testing.T) {
	repo := t.TempDir()
	file := FileScan{
		Path: codemodMissingSource,
		Imports: []ImportBinding{{
			Module:     "lodash",
			ExportName: "map",
			LocalName:  "map",
			Kind:       ImportNamed,
			Location:   report.Location{Line: 1},
		}},
		IdentifierUsage: map[string]int{"map": 1},
	}
	suggestions, skips, warnings := buildCodemodForFile(repo, "lodash", subpathResolver{knownSubpaths: map[string]struct{}{"map": {}}}, file, map[string][]string{})
	if len(suggestions) != 0 || len(skips) != 0 {
		t.Fatalf("expected missing source to avoid suggestions/skips, got %#v %#v", suggestions, skips)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "codemod preview skipped for missing.js") {
		t.Fatalf("expected preview warning, got %#v", warnings)
	}
}

func TestBuildSubpathCodemodReportSkipsSourceThatGrewOversizedBeforePreview(t *testing.T) {
	repo := t.TempDir()
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(`{"exports":{"./map":"./map.js"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	sourcePath := filepath.Join(repo, "index.js")
	if err := os.WriteFile(sourcePath, []byte("import { map } from \"lodash\";\n"), 0o644); err != nil {
		t.Fatalf("write initial source: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("x"+strings.Repeat("y", int(jsSourceReadMaxBytes))), 0o644); err != nil {
		t.Fatalf("grow source after scan: %v", err)
	}

	report, warnings := BuildSubpathCodemodReport(repo, "lodash", depRoot, ScanResult{
		Files: []FileScan{{
			Path: "index.js",
			Imports: []ImportBinding{{
				Module:     "lodash",
				ExportName: "map",
				LocalName:  "map",
				Kind:       ImportNamed,
				Location:   report.Location{Line: 1},
			}},
			IdentifierUsage: map[string]int{"map": 1},
		}},
	})
	if report == nil {
		t.Fatal("expected codemod report")
	}
	if len(report.Suggestions) != 0 || len(report.Skips) != 0 {
		t.Fatalf("expected oversized preview source to produce no suggestions/skips, got %#v", report)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "codemod preview skipped for index.js: source exceeds") {
		t.Fatalf("expected stable oversized preview warning, got %#v", warnings)
	}
}

func TestBuildCodemodForFileWithoutTargetModuleProducesSkip(t *testing.T) {
	assertCodemodSkipReason(t, `import { map } from "lodash";`, 1, subpathResolver{}, codemodReasonNoSubpathTarget)
}

func TestBuildCodemodForFileWithOutOfRangeLineProducesSkip(t *testing.T) {
	assertCodemodSkipReason(t, `import { map } from "lodash";`, 9, subpathResolver{knownSubpaths: map[string]struct{}{"map": {}}}, codemodReasonUnsupportedLine)
}

func TestBuildCodemodForFileWithUnsupportedSyntaxProducesSkip(t *testing.T) {
	assertCodemodSkipReason(t, `import { map, filter } from "lodash";`, 1, subpathResolver{knownSubpaths: map[string]struct{}{"map": {}}}, codemodReasonUnsupportedLine)
}

func TestLoadSourceLinesMissingSource(t *testing.T) {
	lines, warning, loaded := shared.LoadCodemodSourceLines(t.TempDir(), codemodMissingSource, map[string][]string{})
	if loaded || len(lines) != 0 || !strings.Contains(warning, codemodMissingSource) {
		t.Fatalf("expected missing source load failure, got lines=%#v warning=%q loaded=%v", lines, warning, loaded)
	}
}

func TestLoadSourceLinesUsesCache(t *testing.T) {
	cached := map[string][]string{codemodIndexSource: {"cached"}}
	lines, warning, loaded := shared.LoadCodemodSourceLines(t.TempDir(), codemodIndexSource, cached)
	if !loaded || warning != "" || len(lines) != 1 || lines[0] != "cached" {
		t.Fatalf("expected cached source lines, got %#v %q %v", lines, warning, loaded)
	}
}

func TestCodemodSkipReasonBranches(t *testing.T) {
	if code, message := codemodSkipReason(ImportBinding{Kind: ImportDefault}, FileScan{}); code != codemodReasonDefaultImport || !strings.Contains(message, "default imports") {
		t.Fatalf("unexpected default import skip: %q %q", code, message)
	}
	if code, message := codemodSkipReason(ImportBinding{Kind: ImportNamed, ExportName: "map", LocalName: "map"}, FileScan{}); code != codemodReasonUnusedImport || !strings.Contains(message, "unused imports") {
		t.Fatalf("unexpected unused named-import skip: %q %q", code, message)
	}
	if code, message := codemodSkipReason(ImportBinding{Kind: ImportKind("other")}, FileScan{}); code != codemodReasonUnsupportedLine || !strings.Contains(message, "not supported") {
		t.Fatalf("unexpected unsupported-kind skip: %q %q", code, message)
	}
}

func TestCodemodSkipReasonNamespaceAndAliasBranches(t *testing.T) {
	if code, message := codemodSkipReason(ImportBinding{Kind: ImportNamespace, ExportName: "*", LocalName: "*"}, FileScan{}); code != codemodReasonSideEffectImport || !strings.Contains(message, "side-effect imports") {
		t.Fatalf("unexpected namespace wildcard skip: %q %q", code, message)
	}
	if code, message := codemodSkipReason(ImportBinding{Kind: ImportNamespace, ExportName: "*", LocalName: "ns"}, FileScan{}); code != codemodReasonNamespaceImport || !strings.Contains(message, "namespace imports") {
		t.Fatalf("unexpected namespace import skip: %q %q", code, message)
	}
	if code, message := codemodSkipReason(ImportBinding{Kind: ImportNamed, ExportName: "map", LocalName: "mapAlias"}, FileScan{}); code != codemodReasonAliasConflict || !strings.Contains(message, "local-name conflicts") {
		t.Fatalf("unexpected aliased import skip: %q %q", code, message)
	}
}

func TestRewriteImportLineGuardBranches(t *testing.T) {
	if _, ok := rewriteImportLine(`import { map } from "lodash';`, "lodash", "map", lodashMapSubpath); ok {
		t.Fatalf("expected mismatched quote handling to fail import rewrite")
	}
	if _, ok := rewriteImportLine(`const { map } = require("lodash");`, "other", "map", lodashMapSubpath); ok {
		t.Fatalf("expected dependency mismatch to fail require rewrite")
	}
}

func TestNewSubpathResolverIgnoresNonMapExports(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(`{"exports":"./index.js"}`), 0o644); err != nil {
		t.Fatalf("write non-map package.json: %v", err)
	}
	if got := newSubpathResolver(depRoot); len(got.knownSubpaths) != 0 {
		t.Fatalf("expected non-map exports to be ignored, got %#v", got.knownSubpaths)
	}
}

func TestNewSubpathResolverHandlesMissingPackageSurface(t *testing.T) {
	if got := newSubpathResolver(filepath.Join(t.TempDir(), "missing")); len(got.knownSubpaths) != 0 {
		t.Fatalf("expected missing package surface to return empty resolver, got %#v", got.knownSubpaths)
	}
}

func TestNewSubpathResolverReturnsEmptyForBlankRoot(t *testing.T) {
	if got := newSubpathResolver("   "); got.dependencyRoot != "   " || len(got.knownSubpaths) != 0 {
		t.Fatalf("expected blank-root resolver to stay empty, got %#v", got)
	}
}

func TestNewSubpathResolverTracksExplicitExports(t *testing.T) {
	withExports := t.TempDir()
	if err := os.WriteFile(filepath.Join(withExports, "package.json"), []byte(`{"exports":{"./":"./index.js","./map":"./map.js","./*":"./*.js"}}`), 0o644); err != nil {
		t.Fatalf("write package exports: %v", err)
	}
	resolver := newSubpathResolver(withExports)
	if _, ok := resolver.knownSubpaths["map"]; !ok {
		t.Fatalf("expected explicit export subpath to be tracked, got %#v", resolver.knownSubpaths)
	}
	if _, ok := resolver.knownSubpaths[""]; ok {
		t.Fatalf("expected blank subpath to be ignored")
	}
	if _, ok := resolver.knownSubpaths["*"]; ok {
		t.Fatalf("expected wildcard subpath to be ignored")
	}
}

func TestHasResolvableSubpathFileAdditionalBranches(t *testing.T) {
	withExports := t.TempDir()
	nestedDir := filepath.Join(withExports, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, codemodIndexSource), []byte("export default 1\n"), 0o644); err != nil {
		t.Fatalf("write nested index: %v", err)
	}
	if !hasResolvableSubpathFile(withExports, "nested") {
		t.Fatalf("expected nested index lookup to resolve")
	}
	if hasResolvableSubpathFile(withExports, "only-dir") {
		t.Fatalf("expected pure directory candidate not to resolve")
	}
	if hasResolvableSubpathFile(filepath.Join(withExports, "missing"), "nested") {
		t.Fatalf("expected missing dependency root not to resolve")
	}

	blockingFile := filepath.Join(withExports, "file")
	if err := os.WriteFile(blockingFile, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	if hasResolvableSubpathFile(withExports, filepath.Join("file", "nested")) {
		t.Fatalf("expected non-directory path component not to resolve")
	}

	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "escaped.js"), []byte("export default 1\n"), 0o644); err != nil {
		t.Fatalf("write outside source: %v", err)
	}
	outsideSubpath, err := filepath.Rel(withExports, filepath.Join(outsideDir, "escaped"))
	if err != nil {
		t.Fatalf("resolve outside subpath: %v", err)
	}
	if hasResolvableSubpathFile(withExports, outsideSubpath) {
		t.Fatalf("expected traversing subpath not to resolve")
	}
}

func TestHasResolvableSubpathFileAcceptsSymlinkedFileWithinRoot(t *testing.T) {
	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	realPath := filepath.Join(depRoot, "real.js")
	if err := os.WriteFile(realPath, []byte("export default 1\n"), 0o644); err != nil {
		t.Fatalf("write real source: %v", err)
	}
	if err := os.Symlink(realPath, filepath.Join(depRoot, "linked.js")); err != nil {
		t.Skipf("create file symlink: %v", err)
	}

	if !hasResolvableSubpathFile(depRoot, "linked") {
		t.Fatalf("expected symlinked in-root subpath file to resolve")
	}
}

func TestHasResolvableSubpathFileAcceptsSymlinkedDirectoryWithinRoot(t *testing.T) {
	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	realDir := filepath.Join(depRoot, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, codemodIndexSource), []byte("export default 1\n"), 0o644); err != nil {
		t.Fatalf("write real index: %v", err)
	}
	if err := os.Symlink("real", filepath.Join(depRoot, "linked")); err != nil {
		t.Skipf("create directory symlink: %v", err)
	}

	if !hasResolvableSubpathFile(depRoot, "linked") {
		t.Fatalf("expected symlinked in-root subpath directory to resolve")
	}
}

func TestHasResolvableSubpathFileRejectsPinnedStatReadyFailure(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "map.js"), []byte("export default 1\n"), 0o644); err != nil {
		t.Fatalf("write map.js: %v", err)
	}

	originalReady := pinnedPathStatReadyFn
	pinnedPathStatReadyFn = func() error { return os.ErrPermission }
	t.Cleanup(func() {
		pinnedPathStatReadyFn = originalReady
	})

	if hasResolvableSubpathFile(depRoot, "map") {
		t.Fatal("expected pinned stat readiness failure to suppress subpath resolution")
	}
}

func TestHasResolvableSubpathFileRejectsEscapingSymlinkedDirectory(t *testing.T) {
	depRoot := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, codemodIndexSource), []byte("export default 1\n"), 0o644); err != nil {
		t.Fatalf("write outside nested index: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(depRoot, "linked")); err != nil {
		t.Fatalf("create directory symlink: %v", err)
	}

	if hasResolvableSubpathFile(depRoot, "linked") {
		t.Fatalf("expected escaping symlinked subpath directory to be rejected")
	}
}

func TestHasResolvableSubpathFileRejectsParentSwapBetweenValidationAndFinalStat(t *testing.T) {
	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	originalDir := filepath.Join(depRoot, "nested")
	relocatedDir := filepath.Join(depRoot, "nested-real")
	outsideDir := filepath.Join(t.TempDir(), "nested")
	if err := os.MkdirAll(originalDir, 0o755); err != nil {
		t.Fatalf("mkdir original dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originalDir, codemodIndexSource), []byte("export default 1\n"), 0o644); err != nil {
		t.Fatalf("write original index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, codemodIndexSource), []byte("export default 2\n"), 0o644); err != nil {
		t.Fatalf("write alternate index: %v", err)
	}

	originalReady := pinnedPathStatReadyFn
	swapped := false
	pinnedPathStatReadyFn = func() error {
		if swapped {
			return nil
		}
		swapped = true
		if err := os.Rename(originalDir, relocatedDir); err != nil {
			return err
		}
		return os.Symlink(outsideDir, originalDir)
	}
	t.Cleanup(func() {
		pinnedPathStatReadyFn = originalReady
	})

	if hasResolvableSubpathFile(depRoot, "nested") {
		t.Fatalf("expected parent swap to break subpath fallback resolution")
	}
}

func TestBuildSubpathCodemodReportDedupesWarnings(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(`{"exports":{"./map":"./map.js"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	report, warnings := BuildSubpathCodemodReport(t.TempDir(), "lodash", depRoot, ScanResult{
		Files: []FileScan{
			{
				Path: codemodMissingSource,
				Imports: []ImportBinding{{
					Module:     "lodash",
					ExportName: "map",
					LocalName:  "map",
					Kind:       ImportNamed,
					Location:   report.Location{Line: 1},
				}},
				IdentifierUsage: map[string]int{"map": 1},
			},
			{
				Path: codemodMissingSource,
				Imports: []ImportBinding{{
					Module:     "lodash",
					ExportName: "map",
					LocalName:  "map",
					Kind:       ImportNamed,
					Location:   report.Location{Line: 1},
				}},
				IdentifierUsage: map[string]int{"map": 1},
			},
		},
	})
	if report == nil {
		t.Fatalf("expected codemod report")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], codemodMissingSource) {
		t.Fatalf("expected duplicate missing-source warnings to be deduped, got %#v", warnings)
	}
}

func TestBuildSubpathCodemodReportSortsSuggestionsAndIgnoresOtherDependencies(t *testing.T) {
	repo := t.TempDir()
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(`{"exports":{"./map":"./map.js","./filter":"./filter.js"}}`), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "b.js"), []byte("import { filter } from \"lodash\";\n"), 0o644); err != nil {
		t.Fatalf("write b.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.js"), []byte("import { map } from \"lodash\";\n"), 0o644); err != nil {
		t.Fatalf("write a.js: %v", err)
	}

	report, warnings := BuildSubpathCodemodReport(repo, "lodash", depRoot, ScanResult{
		Files: []FileScan{
			{
				Path: "b.js",
				Imports: []ImportBinding{
					{Module: "other", ExportName: "filter", LocalName: "filter", Kind: ImportNamed, Location: report.Location{Line: 1}},
					{Module: "lodash", ExportName: "filter", LocalName: "filter", Kind: ImportNamed, Location: report.Location{Line: 1}},
				},
				IdentifierUsage: map[string]int{"filter": 1},
			},
			{
				Path: "a.js",
				Imports: []ImportBinding{
					{Module: "lodash", ExportName: "map", LocalName: "map", Kind: ImportNamed, Location: report.Location{Line: 1}},
				},
				IdentifierUsage: map[string]int{"map": 1},
			},
		},
	})
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", warnings)
	}
	if report == nil || len(report.Suggestions) != 2 {
		t.Fatalf("expected two sorted suggestions, got %#v", report)
	}
	if report.Suggestions[0].File != "a.js" || report.Suggestions[1].File != "b.js" {
		t.Fatalf("expected suggestions to be sorted by file, got %#v", report.Suggestions)
	}
}

func assertCodemodSkipReason(t *testing.T, sourceLine string, line int, resolver subpathResolver, wantSkip string) {
	t.Helper()

	repo := t.TempDir()
	sourcePath := filepath.Join(repo, codemodIndexSource)
	if err := os.WriteFile(sourcePath, []byte(sourceLine+"\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	file := FileScan{
		Path: codemodIndexSource,
		Imports: []ImportBinding{{
			Module:     "lodash",
			ExportName: "map",
			LocalName:  "map",
			Kind:       ImportNamed,
			Location:   report.Location{Line: line},
		}},
		IdentifierUsage: map[string]int{"map": 1},
	}
	_, skips, warnings := buildCodemodForFile(repo, "lodash", resolver, file, map[string][]string{})
	if len(warnings) != 0 {
		t.Fatalf("expected no warning, got %#v", warnings)
	}
	if len(skips) != 1 || skips[0].ReasonCode != wantSkip {
		t.Fatalf("expected skip %q, got %#v", wantSkip, skips)
	}
}
