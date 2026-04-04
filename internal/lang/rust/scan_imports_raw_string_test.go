package rust

import (
	"strings"
	"testing"
)

func TestParseRustImportsIgnoresUseInsideRawStringLiterals(t *testing.T) {
	scan := &scanResult{UnresolvedImports: map[string]int{}}
	imports := parseRustImports(strings.Join([]string{
		"const DOC: &str = r#\"",
		"use fake_dep::OnlyInLiteral;",
		"\"#;",
		"const BYTES: &[u8] = br##\"",
		"use fake_dep::StillLiteral;",
		"\"##;",
		serdeDeserializeStmt,
		"",
	}, "\n"), srcLibRS, "", map[string]dependencyInfo{"serde": {Canonical: "serde"}}, scan)

	if len(imports) != 1 {
		t.Fatalf("expected one import outside raw string literals, got %#v", imports)
	}
	if imports[0].Dependency != "serde" {
		t.Fatalf("expected serde dependency outside raw string literals, got %#v", imports[0])
	}
	if len(scan.UnresolvedImports) != 0 {
		t.Fatalf("did not expect unresolved imports from raw string literal content, got %#v", scan.UnresolvedImports)
	}
}
