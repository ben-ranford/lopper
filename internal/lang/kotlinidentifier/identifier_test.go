package kotlinidentifier

import (
	"strings"
	"testing"
)

func TestIdentifierHelpers(t *testing.T) {
	for local, want := range map[string]bool{"": false, "1value": false, "data": true, "open": true, "value": true, "π": true, "when": false, "foo.bar": false} {
		if IsBare(local) != want {
			t.Fatalf("bare result for %q", local)
		}
	}
	if got := LastModuleSegment("com.acme.`foo.bar`"); got != "`foo.bar`" {
		t.Fatalf("segment = %q", got)
	}
	if got := LastModuleSegment(""); got != "" {
		t.Fatalf("empty segment = %q", got)
	}
	for content, want := range map[string]bool{"import com.acme.Widget as\t`foo-bar`": true, "import com.acme.Widget as /* note */ `foo-bar`": true, "import com.acme.Widget as   `foo-bar` // note": true, "import com.acme.Widget as `foo-bar` /* note */": true, "import com.acme.Widget as `foo-bar`;  ": true, "import com.acme.Widget as /* note": false, "import com.acme.`foo-bar`": true, "import com.acme.Widget as Foo": false} {
		if got := HasEscapedImportLocal([]byte(content), "foo-bar"); got != want {
			t.Fatalf("escaped import local for %q = %t, want %t", content, got, want)
		}
	}
	if !HasEscapedImportLocal([]byte("import com.acme.Widget as `foo//bar`"), "foo//bar") {
		t.Fatal("expected escaped import local with comment marker")
	}
	content := []byte("package\tcom.example.`when`\nimport\tcom.acme.Widget as `when`\nimport\tcom.acme.`when`.Other\nfun use() { `when`() }\n")
	if uses := CountEscapedLocalUses(content, "when"); uses != 1 {
		t.Fatalf("escaped uses = %d, want 1", uses)
	}
	masked := MaskForFile([]byte("import com.acme.Widget as `foo\"bar`\nfun use() { `foo\"bar`() } // `foo\"bar`\n"), "Main.kt")
	if uses := CountEscapedLocalUses(masked, "foo\"bar"); uses != 1 {
		t.Fatalf("quoted escaped uses = %d", uses)
	}
	masked = MaskForFile([]byte("import com.acme.Widget as `foo`\nval text = \"`foo`\"\nval raw = \"\"\"example \" then `foo`\"\"\"\n/* outer /* inner */ `foo` */\n"), "Main.kt")
	if uses := CountEscapedLocalUses(masked, "foo"); uses != 0 {
		t.Fatalf("declaration-only escaped uses = %d", uses)
	}
	_ = MaskForFile([]byte("val text = \"\\\"\"\n`unfinished"), "Main.kt")
	if got := string(SanitizeImportContent([]byte("import com.acme.Widget as `foo/*bar` // note\n/* hidden */"))); got != "import com.acme.Widget as `foo/*bar`        \n            " {
		t.Fatalf("sanitized imports = %q", got)
	}
	if sanitized := string(SanitizeImportContent([]byte("/* outer /* inner */ hidden */"))); strings.Contains(sanitized, "hidden") {
		t.Fatalf("nested comment was not masked: %q", sanitized)
	}
	if sanitized := string(SanitizeJVMImportContent([]byte("/* outer /* inner */ visible"))); !strings.Contains(sanitized, "visible") {
		t.Fatalf("Java block comment did not close at first terminator: %q", sanitized)
	}
	if sanitized := string(SanitizeImportContent([]byte("\"`\" /* hidden import */"))); strings.Contains(sanitized, "hidden") {
		t.Fatalf("comment after string backtick was not masked: %q", sanitized)
	}
	for _, content := range []string{"\"escaped \\\" quote\" /* hidden */", "'a' /* hidden */", "\"\"\"raw\"\"\" /* hidden */"} {
		if sanitized := string(SanitizeImportContent([]byte(content))); strings.Contains(sanitized, "hidden") {
			t.Fatalf("literal comment was not masked: %q", sanitized)
		}
	}
	if got := NormalizeModuleForLookup("com.acme.`foo.bar`"); got != "com.acme.foo\x00bar" {
		t.Fatalf("normalized module = %q", got)
	}
}
