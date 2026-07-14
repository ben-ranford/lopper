package rust

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

type multilineAliasUsageCase struct {
	name    string
	suffix  string
	wantUse map[string]int
}

func TestMultilineUseAliasDeclarationHits(t *testing.T) {
	const declaration = `use serde::{
    Deserialize as De,
    de::{
        Visitor as Visit,
        value::BorrowedStrDeserializer as Borrowed,
    },
};
`
	tests := []multilineAliasUsageCase{
		{
			name:    "declaration only",
			wantUse: map[string]int{"De": 0, "Visit": 0, "Borrowed": 0},
		},
		{
			name:    "referenced alias",
			suffix:  "\nfn decode(_: De) {}\n",
			wantUse: map[string]int{"De": 1, "Visit": 0, "Borrowed": 0},
		},
	}
	wantLines := map[string]int{"De": 2, "Visit": 4, "Borrowed": 5}
	wantColumns := map[string]int{"De": 20, "Visit": 20, "Borrowed": 43}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertMultilineAliasUsage(t, declaration, test, wantLines, wantColumns)
		})
	}
}

func assertMultilineAliasUsage(t *testing.T, declaration string, test multilineAliasUsageCase, wantLines, wantColumns map[string]int) {
	t.Helper()
	content := []byte(declaration + test.suffix)
	imports := parseRustImportsBytes(content, "src/lib.rs", "", multilineAliasDependencyLookup(), nil)
	if len(imports) != len(test.wantUse) {
		t.Fatalf("expected %d imports, got %#v", len(test.wantUse), imports)
	}

	usage := shared.CountUsage(content, imports)
	for _, imported := range imports {
		assertMultilineAliasImport(t, imported, usage[imported.Local], test.wantUse[imported.Local], wantLines[imported.Local], wantColumns[imported.Local])
	}
}

func assertMultilineAliasImport(t *testing.T, imported importBinding, gotUsage, wantUsage, wantLine, wantColumn int) {
	t.Helper()
	if gotUsage != wantUsage {
		t.Errorf("usage[%q] = %d, want %d", imported.Local, gotUsage, wantUsage)
	}
	if got := imported.Location.Line; got != wantLine {
		t.Errorf("location line for %q = %d, want %d", imported.Local, got, wantLine)
	}
	if got := imported.Location.Column; got != wantColumn {
		t.Errorf("location column for %q = %d, want %d", imported.Local, got, wantColumn)
	}
}

func TestMultilineUseAliasMatchingPathIdentifier(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantUsage int
	}{
		{
			name: "declaration only",
			content: `use serde::{
    de::Deserialize as de,
};
`,
		},
		{
			name: "same line reference after declaration",
			content: `use serde::{
    de::Deserialize as de}; fn decode(_: de) {}
`,
			wantUsage: 1,
		},
		{
			name: "comment token does not hide real reference",
			content: `use serde::{
    de::Deserialize as de,
    // de is not a declaration token or a use
}; fn decode(_: de) {}
`,
			wantUsage: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertMultilinePathAliasUsage(t, test.content, test.wantUsage)
		})
	}
}

func assertMultilinePathAliasUsage(t *testing.T, source string, wantUsage int) {
	t.Helper()
	content := []byte(source)
	imports := parseRustImportsBytes(content, "src/lib.rs", "", multilineAliasDependencyLookup(), nil)
	imported := findMultilinePathAliasImport(t, imports)
	if imported.DeclarationTokenHits != 2 {
		t.Errorf("declaration token hits = %d, want 2", imported.DeclarationTokenHits)
	}
	assertMultilineAliasImport(t, imported, shared.CountUsage(content, imports)["de"], wantUsage, 2, 24)
}

func findMultilinePathAliasImport(t *testing.T, imports []importBinding) importBinding {
	t.Helper()
	var matches []importBinding
	for _, imported := range imports {
		if imported.Local == "de" {
			matches = append(matches, imported)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected one de alias import, got %#v", imports)
	}
	return matches[0]
}

func TestSingleLineUseAliasKeepsStatementLocation(t *testing.T) {
	const content = "use serde::Deserialize as De;\nfn decode(_: De) {}\n"
	imports := parseRustImports(content, "src/lib.rs", "", multilineAliasDependencyLookup(), nil)
	if len(imports) != 1 {
		t.Fatalf("expected one import, got %#v", imports)
	}
	if got := imports[0].Location; got.Line != 1 || got.Column != 5 {
		t.Fatalf("single-line import location = %#v, want line 1 column 5", got)
	}
}

func TestSingleLineUseAliasDeclarationHits(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantUsage int
	}{
		{
			name:    "declaration only",
			content: "use serde::de as de;\n",
		},
		{
			name:      "genuine use on declaration line",
			content:   "use serde::de as de; fn decode(_: de) {}\n",
			wantUsage: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			imports := parseRustImports(test.content, "src/lib.rs", "", multilineAliasDependencyLookup(), nil)
			if len(imports) != 1 {
				t.Fatalf("expected one import, got %#v", imports)
			}
			imported := imports[0]
			if got := imported.Location; got.Line != 1 || got.Column != 5 {
				t.Errorf("single-line import location = %#v, want line 1 column 5", got)
			}
			if imported.DeclarationTokenHits != 2 {
				t.Errorf("declaration token hits = %d, want 2", imported.DeclarationTokenHits)
			}
			if got := shared.CountUsage([]byte(test.content), imports)["de"]; got != test.wantUsage {
				t.Errorf("usage[de] = %d, want %d", got, test.wantUsage)
			}
		})
	}
}

func TestMultilineUseWildcardAdvancesLaterBindingLocation(t *testing.T) {
	const content = `use serde::{
    de::*,
    de,
};
`
	imports := parseRustImports(content, "src/lib.rs", "", multilineAliasDependencyLookup(), nil)
	for _, imported := range imports {
		if imported.Wildcard || imported.Local != "de" {
			continue
		}
		if got := imported.Location; got.Line != 3 || got.Column != 5 {
			t.Fatalf("later de binding location = %#v, want line 3 column 5", got)
		}
		return
	}
	t.Fatalf("later de binding missing from %#v", imports)
}

func TestLocateMultilineUseEntryPaths(t *testing.T) {
	base := useImportContext{Line: 10, Column: 7}
	clause := "serde::Thing,\nOther as Alias"

	first, next := locateMultilineUseEntry(clause, usePathEntry{Symbol: "serde"}, base, 0)
	if first.Line != 10 || first.Column != 7 || next != len("serde") {
		t.Fatalf("unexpected first-line location: context=%#v next=%d", first, next)
	}
	alias, next := locateMultilineUseEntry(clause, usePathEntry{Symbol: "Other", Local: "Alias"}, base, next)
	if alias.Line != 11 || alias.Column != 10 || next != len(clause) {
		t.Fatalf("unexpected alias location: context=%#v next=%d", alias, next)
	}

	wildcard, wildcardNext := locateMultilineUseEntry(clause, usePathEntry{Wildcard: true}, base, next)
	if wildcard.Line != base.Line || wildcard.Column != base.Column || wildcardNext != next {
		t.Fatalf("wildcard location changed: context=%#v next=%d", wildcard, wildcardNext)
	}
	missing, missingNext := locateMultilineUseEntry(clause, usePathEntry{Symbol: "Missing"}, base, 0)
	if missing.Line != base.Line || missing.Column != base.Column || missingNext != 0 {
		t.Fatalf("missing token location changed: context=%#v next=%d", missing, missingNext)
	}
	empty, emptyNext := locateMultilineUseEntry(clause, usePathEntry{}, base, 0)
	if empty.Line != base.Line || empty.Column != base.Column || emptyNext != 0 {
		t.Fatalf("empty token location changed: context=%#v next=%d", empty, emptyNext)
	}
}

func TestFindRustIdentifierTokenBoundaries(t *testing.T) {
	const content = "Deserialize De"
	if got := findRustIdentifierToken(content, "Deserialize", 0); got != 0 {
		t.Fatalf("full token offset = %d, want 0", got)
	}
	if got := findRustIdentifierToken(content, "De", 0); got != len("Deserialize ") {
		t.Fatalf("alias token offset = %d, want %d", got, len("Deserialize "))
	}
	if got := findRustIdentifierToken("Deserialize", "De", 0); got != -1 {
		t.Fatalf("substring offset = %d, want -1", got)
	}
	const collidingAlias = "de::Deserialize as de"
	if got := findRustAliasToken(collidingAlias, "de", 0); got != len("de::Deserialize as ") {
		t.Fatalf("alias token offset = %d, want %d", got, len("de::Deserialize as "))
	}
	if got := findRustAliasToken("de::Deserialize as other", "de", 0); got != -1 {
		t.Fatalf("mismatched alias token offset = %d, want -1", got)
	}
	if got := countRustIdentifierTokens(collidingAlias, "de"); got != 2 {
		t.Fatalf("declaration token count = %d, want 2", got)
	}
	if got := countRustDeclarationTokens(collidingAlias)["de"]; got != 2 {
		t.Fatalf("precomputed declaration token count = %d, want 2", got)
	}
	if got := countRustDeclarationTokens("serde::{Deserialize as De, de::Visitor as De}")["De"]; got != 2 {
		t.Fatalf("precomputed alias declaration token count = %d, want 2", got)
	}
	for _, test := range []struct {
		token string
		start int
	}{
		{token: "", start: 0},
		{token: "De", start: -1},
		{token: "De", start: len(content)},
	} {
		if got := findRustIdentifierToken(content, test.token, test.start); got != -1 {
			t.Errorf("findRustIdentifierToken(%q, %d) = %d, want -1", test.token, test.start, got)
		}
	}
	for _, test := range []struct {
		content string
		token   string
		offset  int
	}{
		{content: content, token: "", offset: 0},
		{content: content, token: "De", offset: -1},
		{content: content, token: "De", offset: len(content)},
		{content: content, token: "Other", offset: 0},
	} {
		if rustIdentifierTokenAt(test.content, test.token, test.offset) {
			t.Errorf("rustIdentifierTokenAt(%q, %q, %d) unexpectedly matched", test.content, test.token, test.offset)
		}
	}
}

func multilineAliasDependencyLookup() map[string]dependencyInfo {
	return map[string]dependencyInfo{"serde": {Canonical: "serde"}}
}
