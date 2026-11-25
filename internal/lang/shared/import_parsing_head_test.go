package shared

import (
	"strings"
	"testing"
)

func TestStripBlockCommentsPreservesTripleQuotedText(t *testing.T) {
	content := []byte(`val text = """
a regular " quote and /* literal marker */
a raw backslash \""""
/* ordinary comment */
val next = 1
`)

	lines := strings.Split(string(StripBlockComments(content)), "\n")
	if lines[0] != `val text = """` || lines[1] != `a regular " quote and /* literal marker */` || lines[2] != `a raw backslash \""""` {
		t.Fatalf("expected triple-quoted text to remain unchanged, got %#v", lines[:3])
	}
	if strings.TrimSpace(lines[3]) != "" || lines[4] != "val next = 1" {
		t.Fatalf("expected only the ordinary comment to be stripped, got %#v", lines)
	}
}

func TestStripBlockCommentsForFilePreservesJavaTextBlocks(t *testing.T) {
	const textLine = `    Quote sequence \""" and /* literal marker */`
	content := []byte("class Example {\n  String text = \"\"\"\n" + textLine + "\n    \"\"\";\n  /* ordinary comment */ int value = 1;\n}\n")

	lines := strings.Split(string(StripBlockCommentsForFile(content, "Example.java")), "\n")
	if lines[2] != textLine || strings.TrimSpace(lines[3]) != `""";` {
		t.Fatalf("expected the complete text block to remain unchanged, got %#v", lines[1:4])
	}
	if got := strings.TrimSpace(lines[4]); got != "int value = 1;" {
		t.Fatalf("expected the ordinary comment after the text block to be stripped, got %q", got)
	}
}

func TestStripBlockCommentsForFilePreservesKotlinBacktickIdentifier(t *testing.T) {
	const declaration = "val `unit's label` = 1"
	content := []byte(declaration + "\n/* ordinary comment */ val next = 2\n")

	lines := strings.Split(string(StripBlockCommentsForFile(content, "Example.kt")), "\n")
	if lines[0] != declaration || strings.TrimSpace(lines[1]) != "val next = 2" {
		t.Fatalf("expected the identifier to remain and the ordinary comment to be stripped, got %#v", lines)
	}
}

func TestStripBlockCommentsPreservesEscapedQuoteMarkers(t *testing.T) {
	content := []byte(`val text = "escaped \" quote and /* literal marker */"
/* ordinary comment */
import com.acme.Widget
`)

	lines := strings.Split(string(StripBlockComments(content)), "\n")
	if lines[0] != `val text = "escaped \" quote and /* literal marker */"` {
		t.Fatalf("expected escaped quoted text to remain unchanged, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "" || lines[2] != "import com.acme.Widget" {
		t.Fatalf("expected only the ordinary comment to be stripped, got %#v", lines)
	}
}

func TestStripBlockCommentsHandlesEndOfInput(t *testing.T) {
	const unfinishedBlock = "/* note"
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "line comment", content: "// note", want: "// note"},
		{name: "unfinished block comment", content: unfinishedBlock, want: strings.Repeat(" ", len(unfinishedBlock))},
		{name: "unfinished quoted string", content: `"note`, want: `"note`},
		{name: "unfinished raw string", content: `"""note`, want: `"""note`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := []byte(test.content)
			if got := string(StripBlockComments(input)); got != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
			if string(input) != test.content {
				t.Error("changed input bytes")
			}
		})
	}
}
