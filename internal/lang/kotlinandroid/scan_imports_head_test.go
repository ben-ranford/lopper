package kotlinandroid

import "testing"

func TestParseJavaImportsWithTextBlock(t *testing.T) {
	source := []byte(`package example;
import com.acme.Widget;

class Example {
    Widget widget;
    String label = """
        Display \""" in the label.
        """;
}
`)
	lookups := dependencyLookups{Aliases: map[string]string{"com.acme": "acme-lib"}}
	result := &scanResult{}

	imports := parseImports(source, "Example.java", "example", lookups, result)
	if len(imports) != 1 || imports[0].Dependency != "acme-lib" || imports[0].Location.Line != 2 {
		t.Fatalf("expected the Java import on line 2, got %#v", imports)
	}
}
