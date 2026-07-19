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

type rustImportUsageExpectation struct {
	declarationTokenHits int
	usage                int
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

func TestFindRustIdentifierTokenPositiveBoundaries(t *testing.T) {
	const content = "Deserialize De"
	assertRustIdentifierTokenOffset(t, content, "Deserialize", 0, 0)
	assertRustIdentifierTokenOffset(t, content, "De", 0, len("Deserialize "))

	const collidingAlias = "de::Deserialize as de"
	if got := findRustAliasToken(collidingAlias, "de", 0); got != len("de::Deserialize as ") {
		t.Fatalf("alias token offset = %d, want %d", got, len("de::Deserialize as "))
	}
	if got := countRustDeclarationTokens(collidingAlias, map[string]struct{}{"de": {}})["de"]; got != 2 {
		t.Fatalf("precomputed declaration token count = %d, want 2", got)
	}
	if got := countRustDeclarationTokens("serde::{Deserialize as De, de::Visitor as De}", map[string]struct{}{"De": {}})["De"]; got != 2 {
		t.Fatalf("precomputed alias declaration token count = %d, want 2", got)
	}
	if got := countRustDeclarationTokens("serde::{Deserialize as De, de::Visitor as De}", map[string]struct{}{"serde": {}, "De": {}}); len(got) != 2 || got["serde"] != 1 || got["De"] != 2 {
		t.Fatalf("unexpected filtered declaration token counts: %#v", got)
	}
	if got := countRustDeclarationTokens("serde::{Deserialize as De, de::Visitor as De}", map[string]struct{}{"Missing": {}}); len(got) != 0 {
		t.Fatalf("expected missing token filter to remain empty, got %#v", got)
	}
}

func TestFindRustIdentifierTokenInvalidStarts(t *testing.T) {
	const content = "Deserialize De"
	for _, test := range []struct {
		token string
		start int
	}{
		{token: "", start: 0},
		{token: "De", start: -1},
		{token: "De", start: len(content)},
	} {
		assertRustIdentifierTokenOffset(t, content, test.token, test.start, -1)
	}
}

func TestFindRustIdentifierTokenNegativeBoundaries(t *testing.T) {
	assertRustIdentifierTokenOffset(t, "Deserialize", "De", 0, -1)
	if got := findRustAliasToken("de::Deserialize as other", "de", 0); got != -1 {
		t.Fatalf("mismatched alias token offset = %d, want -1", got)
	}
	for _, test := range []struct {
		content string
		token   string
		offset  int
	}{
		{content: "Deserialize De", token: "", offset: 0},
		{content: "Deserialize De", token: "De", offset: -1},
		{content: "Deserialize De", token: "De", offset: len("Deserialize De")},
		{content: "Deserialize De", token: "Other", offset: 0},
	} {
		assertRustIdentifierTokenMismatch(t, test.content, test.token, test.offset)
	}
}

func assertRustIdentifierTokenOffset(t *testing.T, content, token string, start, want int) {
	t.Helper()
	if got := findRustIdentifierToken(content, token, start); got != want {
		t.Fatalf("findRustIdentifierToken(%q, %d) = %d, want %d", token, start, got, want)
	}
}

func assertRustIdentifierTokenMismatch(t *testing.T, content, token string, offset int) {
	t.Helper()
	if rustIdentifierTokenAt(content, token, offset) {
		t.Fatalf("rustIdentifierTokenAt(%q, %q, %d) unexpectedly matched", content, token, offset)
	}
}

func TestCollectUseEntryLocalTokens(t *testing.T) {
	entries := []usePathEntry{
		{Symbol: "Deserialize"},
		{Local: "De"},
		{Local: "De"},
		{},
	}
	got := collectUseEntryLocalTokens(entries)
	if len(got) != 2 {
		t.Fatalf("wanted two tracked local tokens, got %#v", got)
	}
	for _, token := range []string{"Deserialize", "De"} {
		if _, ok := got[token]; !ok {
			t.Fatalf("expected token %q to be tracked, got %#v", token, got)
		}
	}
	if got := countRustDeclarationTokens("serde::Deserialize as De", map[string]struct{}{}); len(got) != 0 {
		t.Fatalf("expected empty wanted token set to remain empty, got %#v", got)
	}
}

func TestCountASCIIWordTokenHits(t *testing.T) {
	got := countASCIIWordTokenHits("serde::{de::Visitor as de, fmt::Debug as Debug, MissingX}", map[string]struct{}{"de": {}, "Debug": {}, "Missing": {}, "føø": {}})
	if len(got) != 2 || got["de"] != 2 || got["Debug"] != 2 {
		t.Fatalf("unexpected ASCII word token hits: %#v", got)
	}
	if got["Missing"] != 0 {
		t.Fatalf("expected Missing token to remain absent, got %#v", got)
	}
	if got["føø"] != 0 {
		t.Fatalf("expected non-ASCII token to remain absent, got %#v", got)
	}
}

func TestCountASCIIWordTokenHitsHonorsRustIdentifierBoundaries(t *testing.T) {
	got := countASCIIWordTokenHits("serde::{deø as de, foo as føø}", map[string]struct{}{"de": {}, "foo": {}})
	if len(got) != 2 || got["de"] != 1 || got["foo"] != 1 {
		t.Fatalf("unexpected Unicode-boundary ASCII token hits: %#v", got)
	}
}

func TestMultilineUseAliasSupportsUnicodeLocalNames(t *testing.T) {
	const content = "use serde::de::Deserialize as føø;\nfn decode(_: føø) {}\n"
	imports := parseRustImports(content, "src/lib.rs", "", multilineAliasDependencyLookup(), nil)
	if len(imports) != 1 {
		t.Fatalf("expected one import, got %#v", imports)
	}
	imported := imports[0]
	if imported.Local != "føø" {
		t.Fatalf("local alias = %q, want %q", imported.Local, "føø")
	}
	if imported.DeclarationTokenHits != 1 {
		t.Fatalf("declaration token hits = %d, want 1", imported.DeclarationTokenHits)
	}
	if got := shared.CountUsage([]byte(content), imports)["føø"]; got != 1 {
		t.Fatalf("usage[føø] = %d, want 1", got)
	}
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{name: "alias offset", got: findRustAliasToken("Deserialize as føø", "føø", 0), want: len("Deserialize as ")},
		{name: "identifier offset", got: findRustIdentifierToken("føø bar", "føø", 0), want: 0},
		{name: "substring offset", got: findRustIdentifierToken("føøbar", "føø", 0), want: -1},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	for _, tc := range []struct {
		content string
		offset  int
	}{
		{content: "xføø", offset: 1},
		{content: "føøx", offset: 0},
	} {
		if rustIdentifierTokenAt(tc.content, "føø", tc.offset) {
			t.Fatalf("did not expect unicode boundary match for %q at %d", tc.content, tc.offset)
		}
	}
}

func TestMultilineUseAliasSupportsUnicodeCombiningMarks(t *testing.T) {
	const alias = "e\u0301"
	content := "use serde::de::Deserialize as " + alias + ";\nfn decode(_: " + alias + ") {}\n"
	imports := parseRustImports(content, "src/lib.rs", "", multilineAliasDependencyLookup(), nil)
	if len(imports) != 1 {
		t.Fatalf("expected one import, got %#v", imports)
	}
	imported := imports[0]
	if imported.Local != alias {
		t.Fatalf("local alias = %q, want %q", imported.Local, alias)
	}
	if imported.DeclarationTokenHits != 1 {
		t.Fatalf("declaration token hits = %d, want 1", imported.DeclarationTokenHits)
	}
	if got := shared.CountUsage([]byte(content), imports)[alias]; got != 1 {
		t.Fatalf("usage[%s] = %d, want 1", alias, got)
	}
	if got := findRustAliasToken("Deserialize as "+alias, alias, 0); got != len("Deserialize as ") {
		t.Fatalf("unicode combining alias offset = %d, want %d", got, len("Deserialize as "))
	}
	if got := findRustIdentifierToken(alias+" bar", alias, 0); got != 0 {
		t.Fatalf("unicode combining identifier offset = %d, want 0", got)
	}
	if rustIdentifierTokenAt("x"+alias, alias, 1) {
		t.Fatalf("did not expect combining-mark alias to match inside a larger identifier")
	}
}

func TestMultilineUseAliasSupportsOtherIDLocals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alias string
	}{
		{name: "continue rune", alias: "x·y"},
		{name: "start rune", alias: "ᢅx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := "use serde::{" + tc.alias + "::Thing as " + tc.alias + "};\n"
			imports := parseRustImports(content, "src/lib.rs", "", multilineAliasDependencyLookup(), nil)
			if len(imports) != 1 {
				t.Fatalf("expected one import, got %#v", imports)
			}
			imported := imports[0]
			if imported.Local != tc.alias {
				t.Fatalf("local alias = %q, want %q", imported.Local, tc.alias)
			}
			if imported.DeclarationTokenHits != 2 {
				t.Fatalf("declaration token hits = %d, want 2", imported.DeclarationTokenHits)
			}
			if got := shared.CountUsage([]byte(content), imports)[tc.alias]; got != 0 {
				t.Fatalf("usage[%s] = %d, want 0", tc.alias, got)
			}
		})
	}
}

func TestRustDeclarationCountsASCIIAliasAgainstUnicodePathSegment(t *testing.T) {
	const content = "use serde::deø as de;\n"
	imports := parseRustImports(content, "src/lib.rs", "", multilineAliasDependencyLookup(), nil)
	if len(imports) != 1 {
		t.Fatalf("expected one import, got %#v", imports)
	}
	imported := imports[0]
	if imported.Local != "de" {
		t.Fatalf("local alias = %q, want %q", imported.Local, "de")
	}
	if imported.DeclarationTokenHits != 1 {
		t.Fatalf("declaration token hits = %d, want 1", imported.DeclarationTokenHits)
	}
	if got := shared.CountUsage([]byte(content), imports)["de"]; got != 0 {
		t.Fatalf("usage[de] = %d, want 0", got)
	}
}

func TestRustUnicodeUsageScanKeepsASCIIAliasReference(t *testing.T) {
	const content = "use serde::{deø as de, foo as føø};\nfn f(_: de) {}\n"
	want := map[string]rustImportUsageExpectation{
		"de":  {declarationTokenHits: 1, usage: 1},
		"føø": {declarationTokenHits: 1, usage: 0},
	}
	imports := parseRustImports(content, "src/lib.rs", "", multilineAliasDependencyLookup(), nil)
	if len(imports) != len(want) {
		t.Fatalf("expected %d imports, got %#v", len(want), imports)
	}

	usage := shared.CountUsage([]byte(content), imports)
	for _, imported := range imports {
		assertRustImportUsage(t, imported, usage, want)
	}
}

func assertRustImportUsage(t *testing.T, imported importBinding, usage map[string]int, want map[string]rustImportUsageExpectation) {
	t.Helper()
	expected, ok := want[imported.Local]
	if !ok {
		t.Fatalf("unexpected import local %q", imported.Local)
	}
	if imported.DeclarationTokenHits != expected.declarationTokenHits {
		t.Fatalf("%s declaration token hits = %d, want %d", imported.Local, imported.DeclarationTokenHits, expected.declarationTokenHits)
	}
	if usage[imported.Local] != expected.usage {
		t.Fatalf("usage[%s] = %d, want %d", imported.Local, usage[imported.Local], expected.usage)
	}
}

func TestRustIdentifierHelpersSupportOtherIDRunes(t *testing.T) {
	for _, tc := range []otherIDRustIdentifierCase{
		{
			name:        "continue rune",
			content:     "x·y x·yz",
			token:       "x·y",
			wantOffset:  0,
			wantCount:   1,
			wantHits:    2,
			wantNoMatch: "x·yz",
		},
		{
			name:        "start rune",
			content:     "ᢅx ᢅxy",
			token:       "ᢅx",
			wantOffset:  0,
			wantCount:   1,
			wantHits:    2,
			wantNoMatch: "ᢅxy",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRustIdentifierHelperCase(t, tc)
		})
	}
}

type otherIDRustIdentifierCase struct {
	name        string
	content     string
	token       string
	wantOffset  int
	wantCount   int
	wantHits    int
	wantNoMatch string
}

func assertRustIdentifierHelperCase(t *testing.T, tc otherIDRustIdentifierCase) {
	t.Helper()
	if got := findRustIdentifierToken(tc.content, tc.token, 0); got != tc.wantOffset {
		t.Fatalf("findRustIdentifierToken(%q) = %d, want %d", tc.token, got, tc.wantOffset)
	}
	if got := findRustIdentifierToken(tc.wantNoMatch, tc.token, 0); got != -1 {
		t.Fatalf("embedded match offset = %d, want -1", got)
	}
	if got := countRustDeclarationTokens(tc.content, map[string]struct{}{tc.token: {}})[tc.token]; got != tc.wantCount {
		t.Fatalf("countRustDeclarationTokens(%q) = %d, want %d", tc.token, got, tc.wantCount)
	}
	if got := countRustDeclarationTokens(tc.token+"::Thing as "+tc.token, map[string]struct{}{tc.token: {}})[tc.token]; got != tc.wantHits {
		t.Fatalf("countRustDeclarationTokens(%q) = %d, want %d", tc.token, got, tc.wantHits)
	}
}

func TestUnicodeTokenHelpersSkipMismatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{name: "wildcard advance without wildcard", got: advancePastRustUseWildcard("serde::de", 0), want: 0},
		{name: "wildcard advance with negative start", got: advancePastRustUseWildcard("serde::*", -1), want: -1},
		{name: "alias second match offset", got: findRustAliasToken("Deserialize as other as føø", "føø", 0), want: len("Deserialize as other as ")},
		{name: "alias skips non-identifier candidate", got: findRustAliasToken("Deserialize as = as føø", "føø", 0), want: len("Deserialize as = as ")},
		{name: "unicode identifier count", got: countRustDeclarationTokens("føø bar føø", map[string]struct{}{"føø": {}})["føø"], want: 2},
		{name: "missing unicode identifier count", got: countRustDeclarationTokens("bar baz", map[string]struct{}{"føø": {}})["føø"], want: 0},
		{name: "unicode identifier offset after search start", got: findRustIdentifierToken("xx føø", "føø", 1), want: len("xx ")},
		{name: "missing unicode identifier offset", got: findRustIdentifierToken("xx", "føø", 0), want: -1},
		{name: "identifier skips embedded match before full token", got: findRustIdentifierToken("détail dé", "dé", 0), want: len("détail ")},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
	if got := countRustDeclarationTokens("Ⅰvalue as Ⅰvalue, e\u0301 as e\u0301", map[string]struct{}{"Ⅰvalue": {}, "e\u0301": {}}); len(got) != 2 || got["Ⅰvalue"] != 2 || got["e\u0301"] != 2 {
		t.Fatalf("unexpected unicode declaration token counts: %#v", got)
	}
	invalidClause := string([]byte{0xff, ' ', 'f', 'o', 'o', ' ', '1', '2', '3'})
	if got := countRustDeclarationTokens(invalidClause, map[string]struct{}{"foo": {}})["foo"]; got != 1 {
		t.Fatalf("invalid utf-8 declaration token count = %d, want 1", got)
	}
}

func multilineAliasDependencyLookup() map[string]dependencyInfo {
	return map[string]dependencyInfo{"serde": {Canonical: "serde"}}
}
