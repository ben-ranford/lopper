package shared

import "testing"

func TestMatchJVMImportSupportsKotlinEscapedIdentifiers(t *testing.T) {
	matches := MatchJVMImport("import com.example.`when`.Widget as `type`")
	if len(matches) != JVMImportMatchGroups {
		t.Fatalf("expected escaped Kotlin import match, got %#v", matches)
	}
	if matches[1] != "com.example.`when`.Widget" || matches[3] != "`type`" {
		t.Fatalf("unexpected Kotlin import match %#v", matches)
	}
}

func TestMatchJVMImportPreservesJavaImports(t *testing.T) {
	matches := MatchJVMImport("import static com.example.Widget.*;")
	if len(matches) != JVMImportMatchGroups || matches[1] != "com.example.Widget" || matches[2] != ".*" {
		t.Fatalf("unexpected Java import match %#v", matches)
	}
}
