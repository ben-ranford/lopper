package kotlinidentifier

import "testing"

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
	for content, want := range map[string]bool{"import com.acme.Widget as\t`foo-bar`": true, "import com.acme.Widget as   `foo-bar` // note": true, "import com.acme.Widget as `foo-bar` /* note */": true, "import com.acme.`foo-bar`": true, "import com.acme.Widget as Foo": false} {
		if got := HasEscapedImportLocal([]byte(content), "foo-bar"); got != want {
			t.Fatalf("escaped import local = %t, want %t", got, want)
		}
	}
	content := []byte("package\tcom.example.`when`\nimport\tcom.acme.Widget as `when`\nimport\tcom.acme.`when`.Other\nfun use() { `when`() }\n")
	if uses := CountEscapedLocalUses(content, "when"); uses != 1 {
		t.Fatalf("escaped uses = %d, want 1", uses)
	}
	masked := MaskForFile([]byte("import com.acme.Widget as `foo\"bar`\nfun use() { `foo\"bar`() } // `foo\"bar`\n"), "Main.kt")
	if uses := CountEscapedLocalUses(masked, "foo\"bar"); uses != 1 {
		t.Fatalf("quoted escaped uses = %d", uses)
	}
}
