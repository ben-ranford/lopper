package rust

import (
	"slices"
	"strings"
	"testing"
)

func TestRustImportScannerSkipsOversizeClauseAndKeepsFollowingImport(t *testing.T) {
	const statementLimit = 64 * 1024
	content := []byte("use first::" + strings.Repeat(" ", statementLimit) + "Item;\nuse second::Item;\n")
	var clauses []string
	scanRustImportStatements(content, true, func(kind rustImportKind, stmt rustImportStatement) {
		if kind == rustImportUse {
			clauses = append(clauses, string(stmt.Clause))
		}
	})
	if !slices.Equal(clauses, []string{"second::Item"}) {
		t.Fatalf("expected following import to be kept, got %d imports", len(clauses))
	}
}
