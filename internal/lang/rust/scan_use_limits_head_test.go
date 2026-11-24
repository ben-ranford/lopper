package rust

import (
	"slices"
	"testing"
)

func parseUseClause(clause string) []usePathEntry {
	entries, _ := parseUseClauseWithLimits(clause, maxUseClauseDepth, maxUseClauseEntries)
	return entries
}

func splitTopLevel(value string, sep rune) []string {
	return slices.Collect(topLevelUseSegments(value, sep))
}

func expandUsePart(part, prefix string, out *[]usePathEntry) {
	parser := useClauseParser{entries: *out, maxDepth: maxUseClauseDepth, maxEntries: maxUseClauseEntries}
	parser.expand(part, prefix, 0)
	*out = parser.entries
}

func expandUseBraceGroup(part, prefix string, out *[]usePathEntry) bool {
	parser := useClauseParser{entries: *out, maxDepth: maxUseClauseDepth, maxEntries: maxUseClauseEntries}
	matched := parser.expandBraceGroup(part, prefix, 0)
	*out = parser.entries
	return matched
}

func expandUsePrefixedBraceGroup(part, prefix string, out *[]usePathEntry) bool {
	parser := useClauseParser{entries: *out, maxDepth: maxUseClauseDepth, maxEntries: maxUseClauseEntries}
	matched := parser.expandPrefixedBraceGroup(part, prefix, 0)
	*out = parser.entries
	return matched
}

func TestParseUseClauseLimitsNestedGroupsAndEntries(t *testing.T) {
	entries, capped := parseUseClauseWithLimits("serde::{de::{Deserialize}}", 1, 4)
	if !capped || len(entries) != 0 {
		t.Fatalf("expected nested group parsing to stop at the depth limit, got capped=%v entries=%#v", capped, entries)
	}

	entries, capped = parseUseClauseWithLimits("serde::{Deserialize, Serialize}", 4, 1)
	if !capped || len(entries) != 1 || entries[0].Path != "serde::Deserialize" {
		t.Fatalf("expected entry limit to preserve the first ordinary import, got capped=%v entries=%#v", capped, entries)
	}
}
