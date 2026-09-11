package kotlin

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestMatchImportAndLocalName(t *testing.T) {
	tests := []struct {
		line      string
		module    string
		wantLocal string
		wantMatch bool
	}{
		{"import com.acme.Widget", "com.acme.Widget", "Widget", true},
		{"import com.acme.Widget as `when`", "com.acme.Widget", "when", true},
		{"import\tcom.acme.`when`", "com.acme.`when`", "when", true},
		{"import com.acme.Widget as `Alias`", "com.acme.Widget", "Alias", true},
		{"import com.acme.Widget.*", "com.acme.Widget", "*", true},
		{"import com.acme.`my type`", "", "", false},
		{"import com.acme.Widget as `my type`", "", "", false},
		{"not an import", "", "", false},
	}
	for _, test := range tests {
		t.Run(test.line, func(t *testing.T) {
			matches := MatchImport(test.line)
			if got := IsImportMatch(matches); got != test.wantMatch {
				t.Fatalf("IsImportMatch() = %v, want %v", got, test.wantMatch)
			}
			if test.wantMatch && LocalName(matches, test.module) != test.wantLocal {
				t.Fatalf("LocalName() = %q, want %q", LocalName(matches, test.module), test.wantLocal)
			}
		})
	}
}

func TestMatchImportModule(t *testing.T) {
	matches, module, ok := MatchImportModule("import com.acme.`when`.Widget as `when`")
	if !ok || module != "com.acme.`when`.Widget" || LocalName(matches, module) != "when" {
		t.Fatalf("expected escaped import module and local, got module %q ok=%v matches %#v", module, ok, matches)
	}
	if _, _, ok := MatchImportModule("import"); ok {
		t.Fatal("expected empty import module to be rejected")
	}
	if _, _, ok := MatchImportModule("val text = \"import com.acme.Widget\""); ok {
		t.Fatal("expected non-import source line to be rejected")
	}
}

func TestCountUsageForEscapedImports(t *testing.T) {
	imports := []shared.ImportRecord{{Local: "when", Location: report.Location{File: "App.kt", Line: 2}}, {Local: "Alias", Location: report.Location{File: "App.kt", Line: 3}}}
	content := []byte("package demo.`when`\nimport\tcom.acme.Widget as `when`\nimport com.acme.Other as `Alias`\n// `when`()\nval text = \"`when`()\"\nwhen { else -> `when`() }\nAlias()\n")
	usage := CountUsage(content, imports)
	if usage["when"] != 1 {
		t.Fatalf("escaped hard keyword usage = %d, want 1", usage["when"])
	}
	if usage["Alias"] != 1 {
		t.Fatalf("legal escaped alias bare usage = %d, want 1", usage["Alias"])
	}
}

func TestCountUsageForDirectEscapedTerminalImport(t *testing.T) {
	imports := []shared.ImportRecord{{Local: "when", Location: report.Location{File: "App.kt", Line: 1}}}
	content := []byte("import com.acme.`when`\n`when`()\n")
	usage := CountUsage(content, imports)
	if usage["when"] != 1 {
		t.Fatalf("direct escaped terminal import usage = %d, want 1", usage["when"])
	}
}

func TestCountUsageHandlesNoEscapedBindingsAndMixedBindings(t *testing.T) {
	if usage := CountUsage([]byte("import com.acme.Widget as Alias\nAlias()\n"), []shared.ImportRecord{{Local: "Alias", Location: report.Location{File: "App.kt", Line: 1}}}); usage["Alias"] != 1 {
		t.Fatalf("bare alias usage = %d, want 1", usage["Alias"])
	}

	content := []byte("import com.acme.Widget as `when`\nimport com.acme.Other as when\nwhen\n`when`()\n")
	usage := CountUsage(content, []shared.ImportRecord{{Local: "when", Location: report.Location{File: "App.kt", Line: 1}}, {Local: "when", Location: report.Location{File: "App.kt", Line: 2}}})
	if usage["when"] != 2 {
		t.Fatalf("mixed binding usage = %d, want 2", usage["when"])
	}
}

func TestEscapedIdentifierAt(t *testing.T) {
	if _, _, ok := escapedIdentifierAt([]byte("plain"), 0); ok {
		t.Fatal("expected plain text to be ignored")
	}
	if _, _, ok := escapedIdentifierAt([]byte("`my type`"), 0); ok {
		t.Fatal("expected whitespace identifier to be ignored")
	}
	local, end, ok := escapedIdentifierAt([]byte("`when`"), 0)
	if !ok || local != "when" || end != 5 {
		t.Fatalf("escapedIdentifierAt() = (%q, %d, %v)", local, end, ok)
	}
}
