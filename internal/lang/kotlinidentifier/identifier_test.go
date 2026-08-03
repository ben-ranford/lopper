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
	for content, want := range map[string]bool{"import com.acme.Widget as\t`foo-bar`": true, "import com.acme.Widget as   `foo-bar`": true, "import com.acme.`foo-bar`": true, "import com.acme.Widget as Foo": false} {
		if got := HasEscapedImportLocal([]byte(content), "foo-bar"); got != want {
			t.Fatalf("escaped import local = %t, want %t", got, want)
		}
	}
	content := []byte("package com.example.`when`\nimport com.acme.Widget as `when`\nimport com.acme.`when`.Other\nfun use() { `when`() }\n")
	if uses := CountEscapedLocalUses(content, "when"); uses != 1 {
		t.Fatalf("escaped uses = %d, want 1", uses)
	}
}
