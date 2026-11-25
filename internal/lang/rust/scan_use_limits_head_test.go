package rust

import (
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func parseUseClause(clause string) []usePathEntry {
	entries, _ := parseUseClauseWithLimits(clause, useClauseLimits{maxDepth: maxUseClauseDepth, maxEntries: maxUseClauseEntries})
	return entries
}

func splitTopLevel(value string, sep rune) []string {
	return slices.Collect(topLevelUseSegments(value, sep))
}

func expandUsePart(part, prefix string, out *[]usePathEntry) {
	parser := useClauseParser{entries: *out, limits: useClauseLimits{maxDepth: maxUseClauseDepth, maxEntries: maxUseClauseEntries}}
	parser.expand(part, prefix, 0)
	*out = parser.entries
}

func expandUseBraceGroup(part, prefix string, out *[]usePathEntry) bool {
	parser := useClauseParser{entries: *out, limits: useClauseLimits{maxDepth: maxUseClauseDepth, maxEntries: maxUseClauseEntries}}
	matched := parser.expandBraceGroup(part, prefix, 0)
	*out = parser.entries
	return matched
}

func expandUsePrefixedBraceGroup(part, prefix string, out *[]usePathEntry) bool {
	parser := useClauseParser{entries: *out, limits: useClauseLimits{maxDepth: maxUseClauseDepth, maxEntries: maxUseClauseEntries}}
	matched := parser.expandPrefixedBraceGroup(part, prefix, 0)
	*out = parser.entries
	return matched
}

func TestParseUseClauseLimitsNestedGroupsAndEntries(t *testing.T) {
	entries, capped := parseUseClauseWithLimits("serde::{de::{Deserialize}}", useClauseLimits{maxDepth: 1, maxEntries: 4})
	if !capped || len(entries) != 0 {
		t.Fatalf("expected nested group parsing to stop at the depth limit, got capped=%v entries=%#v", capped, entries)
	}

	entries, capped = parseUseClauseWithLimits("serde::{Deserialize, Serialize}", useClauseLimits{maxDepth: 4, maxEntries: 1})
	if !capped || len(entries) != 1 || entries[0].Path != "serde::Deserialize" {
		t.Fatalf("expected entry limit to preserve the first ordinary import, got capped=%v entries=%#v", capped, entries)
	}
}

func TestUseClauseLimitReportsPerFileCoverageAndKeepsEarlierImport(t *testing.T) {
	scan := &scanResult{
		UnresolvedImports: make(map[string]int),
		LocalModuleCache:  make(map[string]bool),
	}
	content := []byte("use serde::{Deserialize, Serialize};\nuse serde::Serialize;")
	dependencyLookup := map[string]dependencyInfo{"serde": {Canonical: "serde"}}
	limits := useClauseLimits{maxDepth: 4, maxEntries: 1}
	_, imports := collectRustImportsWithUseClauseLimits(content, "src/lib.rs", "", dependencyLookup, scan, true, limits)
	if len(imports) != 2 || imports[0].Dependency != "serde" || imports[0].Name != "Deserialize" || imports[1].Name != "Serialize" {
		t.Fatalf("expected imports before and after the cap to remain available, got %#v", imports)
	}
	if len(scan.Warnings) != 1 || scan.Warnings[0] != rustUseStatementLimitWarning {
		t.Fatalf("expected the per-file use limit warning, got %#v", scan.Warnings)
	}
	if len(scan.CoverageGaps) != 1 || scan.CoverageGaps[0].Code != report.CoverageGapRustUseStatementLimit ||
		scan.CoverageGaps[0].Path != "src/lib.rs" {
		t.Fatalf("expected one per-file coverage gap, got %#v", scan.CoverageGaps)
	}

	_, importsWithoutScan := collectRustImportsWithUseClauseLimits(content, "src/lib.rs", "", dependencyLookup, nil, true, limits)
	if !slices.Equal(importsWithoutScan, imports) {
		t.Fatalf("expected the same imports without the coverage collector, got %#v", importsWithoutScan)
	}
}
