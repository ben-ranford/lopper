package js

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	lines, warning, loaded := loadSourceLines(t.TempDir(), codemodMissingSource, map[string][]string{})
	if loaded || len(lines) != 0 || !strings.Contains(warning, codemodMissingSource) {
		t.Fatalf("expected missing source load failure, got lines=%#v warning=%q loaded=%v", lines, warning, loaded)
	}
}

func TestLoadSourceLinesUsesCache(t *testing.T) {
	cached := map[string][]string{codemodIndexSource: {"cached"}}
	lines, warning, loaded := loadSourceLines(t.TempDir(), codemodIndexSource, cached)
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
