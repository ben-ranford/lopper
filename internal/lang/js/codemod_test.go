package js

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	lodashFixturePackageJSON = "{\n  \"main\": \"index.js\",\n  \"exports\": {\n    \".\": \"./index.js\",\n    \"./map\": \"./map.js\"\n  }\n}\n"
	mapImportFixtureSource   = "import { map } from \"lodash\";\nmap([1], (x) => x)\n"
)

func TestAdapterAnalyseSuggestOnlyCodemodPreview(t *testing.T) {
	repo, sourcePath, original := setupLodashFixture(t, mapImportFixtureSource)

	reportData := analyseSuggestOnly(t, repo)
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected 1 dependency report, got %d", len(reportData.Dependencies))
	}
	codemod := reportData.Dependencies[0].Codemod
	if codemod == nil {
		t.Fatalf("expected codemod report")
	}
	if codemod.Mode != codemodModeSuggestOnly {
		t.Fatalf("expected codemod mode %q, got %q", codemodModeSuggestOnly, codemod.Mode)
	}
	if len(codemod.Suggestions) != 1 {
		t.Fatalf("expected one codemod suggestion, got %#v", codemod.Suggestions)
	}
	suggestion := codemod.Suggestions[0]
	if suggestion.ToModule != "lodash/map" {
		t.Fatalf("expected lodash/map replacement, got %#v", suggestion)
	}
	if !strings.Contains(suggestion.Patch, "@@ -1 +1 @@") {
		t.Fatalf("expected deterministic one-line patch hunk, got %q", suggestion.Patch)
	}
	if !strings.Contains(suggestion.Patch, "-import { map } from \"lodash\";") || !strings.Contains(suggestion.Patch, "+import map from \"lodash/map\";") {
		t.Fatalf("unexpected patch preview: %q", suggestion.Patch)
	}

	contentAfter, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read source after analyse: %v", err)
	}
	if string(contentAfter) != original {
		t.Fatalf("expected suggest-only mode to avoid mutating source, got %q", string(contentAfter))
	}
}

func TestAdapterAnalyseSuggestOnlySkipsUnsafeTransforms(t *testing.T) {
	repo, _, _ := setupLodashFixture(t, strings.Join([]string{"import \"lodash\"", "import * as lodash from \"lodash\"", "import { map as mapAlias } from \"lodash\"", "mapAlias([1], (x) => x)", ""}, "\n"))

	reportData := analyseSuggestOnly(t, repo)
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected single dependency report, got %d", len(reportData.Dependencies))
	}
	codemod := reportData.Dependencies[0].Codemod
	if codemod == nil {
		t.Fatalf("expected codemod report")
	}
	if len(codemod.Suggestions) != 0 {
		t.Fatalf("expected no safe suggestions, got %#v", codemod.Suggestions)
	}

	reasons := make([]string, 0, len(codemod.Skips))
	for _, skip := range codemod.Skips {
		reasons = append(reasons, skip.ReasonCode)
	}
	for _, expected := range []string{codemodReasonSideEffectImport, codemodReasonNamespaceImport, codemodReasonAliasConflict} {
		if !slices.Contains(reasons, expected) {
			t.Fatalf("expected skip reason %q, got %#v", expected, reasons)
		}
	}
}

func TestSuggestOnlyPatchPreviewAppliesCleanlyOnFixture(t *testing.T) {
	repo, _, original := setupLodashFixture(t, mapImportFixtureSource)

	reportData := analyseSuggestOnly(t, repo)
	codemod := reportData.Dependencies[0].Codemod
	if codemod == nil || len(codemod.Suggestions) == 0 {
		t.Fatalf("expected codemod suggestions, got %#v", codemod)
	}
	patch := codemod.Suggestions[0].Patch
	updated, err := applySingleLineUnifiedPatch(original, patch)
	if err != nil {
		t.Fatalf("expected patch to apply cleanly: %v\npatch:\n%s", err, patch)
	}
	if !strings.Contains(updated, "import map from \"lodash/map\";") {
		t.Fatalf("expected patched import line, got %q", updated)
	}
}

func TestSuggestOnlyBehavioralParityAfterApplyingPatch(t *testing.T) {
	repo, sourcePath, original := setupLodashFixture(t, mapImportFixtureSource)
	beforeMapUsage := mapIdentifierUsage(t, repo, "before")
	before := analyseDependency(t, repo)
	applyFirstSuggestionPatch(t, repo, sourcePath, original)
	after := analyseDependency(t, repo)
	afterMapUsage := mapIdentifierUsage(t, repo, "after")
	if beforeMapUsage != afterMapUsage {
		t.Fatalf("expected local map identifier usage parity, before=%d after=%d", beforeMapUsage, afterMapUsage)
	}
	if len(before.Dependencies[0].UsedImports) != len(after.Dependencies[0].UsedImports) {
		t.Fatalf("expected same used import count before/after patch, before=%d after=%d", len(before.Dependencies[0].UsedImports), len(after.Dependencies[0].UsedImports))
	}
}

func TestCodemodSuggestionOrder(t *testing.T) {
	early := report.CodemodSuggestion{
		File:       "a.js",
		Line:       2,
		ImportName: "map",
		ToModule:   "lodash/map",
	}
	late := report.CodemodSuggestion{
		File:       "b.js",
		Line:       1,
		ImportName: "filter",
		ToModule:   "lodash/filter",
	}
	if !(codemodSuggestionOrder(early) < codemodSuggestionOrder(late)) {
		t.Fatalf("expected lexical codemod suggestion ordering")
	}
}

func TestHasResolvableSubpathFile(t *testing.T) {
	depRoot := t.TempDir()
	if hasResolvableSubpathFile(depRoot, "map") {
		t.Fatalf("did not expect missing subpath to resolve")
	}

	if err := os.WriteFile(filepath.Join(depRoot, "map.js"), []byte("export default 1\n"), 0o644); err != nil {
		t.Fatalf("write map.js: %v", err)
	}
	if !hasResolvableSubpathFile(depRoot, "map") {
		t.Fatalf("expected .js subpath resolution")
	}

	nested := filepath.Join(depRoot, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "index.cjs"), []byte("module.exports = {}\n"), 0o644); err != nil {
		t.Fatalf("write nested index: %v", err)
	}
	if !hasResolvableSubpathFile(depRoot, "nested") {
		t.Fatalf("expected nested index subpath resolution")
	}
}

func setupLodashFixture(t *testing.T, source string) (repo string, sourcePath string, original string) {
	t.Helper()
	repo = t.TempDir()
	sourcePath = filepath.Join(repo, "index.js")
	if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	depRoot := filepath.Join(repo, "node_modules", "lodash")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(lodashFixturePackageJSON), 0o644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "index.js"), []byte("export { map } from './map.js'\n"), 0o644); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "map.js"), []byte("export default function map() {}\n"), 0o644); err != nil {
		t.Fatalf("write map.js: %v", err)
	}
	return repo, sourcePath, source
}

func analyseSuggestOnly(t *testing.T, repo string) report.Report {
	t.Helper()
	result, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:       repo,
		Dependency:     "lodash",
		SuggestOnly:    true,
		RuntimeProfile: "node-import",
	})
	if err != nil {
		t.Fatalf("analyse suggest-only: %v", err)
	}
	return result
}

func analyseDependency(t *testing.T, repo string) report.Report {
	t.Helper()
	result, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "lodash",
	})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(result.Dependencies) != 1 || len(result.Dependencies[0].UsedImports) == 0 {
		t.Fatalf("expected used imports for lodash, got %#v", result.Dependencies)
	}
	return result
}

func mapIdentifierUsage(t *testing.T, repo, stage string) int {
	t.Helper()
	scan, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan %s patch: %v", stage, err)
	}
	if len(scan.Files) == 0 {
		t.Fatalf("expected scanned files %s patch", stage)
	}
	return scan.Files[0].IdentifierUsage["map"]
}

func applyFirstSuggestionPatch(t *testing.T, repo, sourcePath, original string) {
	t.Helper()
	suggest := analyseSuggestOnly(t, repo)
	codemod := suggest.Dependencies[0].Codemod
	if codemod == nil || len(codemod.Suggestions) == 0 {
		t.Fatalf("expected codemod suggestion for patch apply")
	}
	patchedContent, err := applySingleLineUnifiedPatch(original, codemod.Suggestions[0].Patch)
	if err != nil {
		t.Fatalf("apply patch: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte(patchedContent), 0o644); err != nil {
		t.Fatalf("write patched source: %v", err)
	}
}

func applySingleLineUnifiedPatch(content, patch string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	patchLines := strings.Split(strings.ReplaceAll(strings.TrimSpace(patch), "\r\n", "\n"), "\n")
	if len(patchLines) < 5 {
		return "", fmt.Errorf("invalid patch, expected at least 5 lines")
	}
	hunk := patchLines[2]
	if !strings.HasPrefix(hunk, "@@ -") {
		return "", fmt.Errorf("invalid hunk header: %q", hunk)
	}
	parts := strings.Fields(strings.Trim(hunk, "@ "))
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid hunk parts: %q", hunk)
	}
	oldPos := strings.TrimPrefix(parts[0], "-")
	linePart := strings.SplitN(oldPos, ",", 2)[0]
	line, err := strconv.Atoi(linePart)
	if err != nil || line <= 0 {
		return "", fmt.Errorf("invalid line number in hunk: %q", oldPos)
	}
	if line > len(lines) {
		return "", fmt.Errorf("hunk line out of range: %d", line)
	}
	oldLine := strings.TrimPrefix(patchLines[3], "-")
	newLine := strings.TrimPrefix(patchLines[4], "+")
	if lines[line-1] != oldLine {
		return "", fmt.Errorf("source line mismatch at %d: got %q want %q", line, lines[line-1], oldLine)
	}
	lines[line-1] = newLine
	return strings.Join(lines, "\n"), nil
}
