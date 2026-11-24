package rust

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func parseRustImportStatement(content []byte, lineStart, lineEnd, line int, includeUse bool) (rustImportKind, rustImportStatement, bool) {
	scan := rustUseTerminator{remaining: len(content)}
	return scan.parseRustImportStatement(content, lineStart, lineEnd, line, includeUse)
}

func buildRustUseStatement(content []byte, lineStart, line, statementStart int, statement []byte) (rustImportStatement, bool) {
	scan := rustUseTerminator{remaining: len(content)}
	return scan.buildRustUseStatement(content, lineStart, line, statementStart, statement)
}

func TestRustUseStatementLimitReportsSourceCoverage(t *testing.T) {
	repo := t.TempDir()
	const source = "src/lib.rs"
	writeFile(t, filepath.Join(repo, cargoTomlName), "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n[dependencies]\nserde = \"1\"\n")
	content := "use first::" + strings.Repeat(" ", maxRustUseStatementBytes) + "Item;\n" +
		"use serde::Serialize;\nfn record<T: Serialize>() {}\n"
	writeFile(t, filepath.Join(repo, filepath.FromSlash(source)), content)

	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "serde"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dependencies) != 1 || len(result.Dependencies[0].UsedImports) != 1 {
		t.Fatalf("expected the following import to appear in the dependency report, got %#v", result.Dependencies)
	}
	if len(result.CoverageGaps) != 1 {
		t.Fatalf("expected one coverage gap, got %#v", result.CoverageGaps)
	}
	gap := result.CoverageGaps[0]
	if gap.Code != report.CoverageGapRustUseStatementLimit || gap.Language != rustAdapterID || gap.Path != source ||
		len(gap.Evidence) != 1 || !slices.Contains(result.Warnings, gap.Evidence[0]) {
		t.Fatalf("expected a Rust statement limit gap with its warning, got %#v", gap)
	}
}

func TestRustUseTerminatorRecordsWorkLimit(t *testing.T) {
	content := []byte("use serde::Deserialize;")
	scan := rustUseTerminator{remaining: 1}
	if _, ok := scan.buildRustUseStatement(content, 0, 1, 0, content); ok || !scan.limited {
		t.Fatalf("expected the exhausted work budget to reject the statement and mark the scan limited")
	}
}
