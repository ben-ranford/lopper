package shared

import (
	"slices"
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

func TestStripBlockCommentsForFilePreservesKotlinTemplates(t *testing.T) {
	const nestedComment = "/* optional /* note */ */"
	source := []string{
		`val title = "${"Reader's guide"}"`,
		"val `named label` = \"Daily\"",
		`val label = """${listOf(`,
		`    title, // display title`,
		"    " + nestedComment + ` buildString { append('}') },`,
		"    `named label`,",
		`    """Section ${"Intro"}"""`,
		`).joinToString()}"""`,
		`/* ordinary comment */ val next = 2`,
	}
	content := []byte(strings.Join(source, "\n"))
	lines := strings.Split(string(StripBlockCommentsForFile(content, "Example.kt")), "\n")
	want := slices.Clone(source)
	want[4] = strings.Replace(want[4], nestedComment, strings.Repeat(" ", len(nestedComment)), 1)
	if !slices.Equal(lines[:len(lines)-1], want[:len(want)-1]) {
		t.Fatalf("expected only the comment inside the template expression to be stripped, got %#v", lines)
	}
	if got := strings.TrimSpace(lines[len(lines)-1]); got != "val next = 2" {
		t.Fatalf("expected the ordinary comment after the templates to be stripped, got %q", got)
	}
}

func TestStripBlockCommentsForFilePreservesKotlinQuotedTemplateReferences(t *testing.T) {
	const escaped = "`item \"label\"`"
	source := []string{
		"val " + escaped + " = \"Daily\"",
		"val title = \"$" + escaped + " /* literal note */\"",
		"val rawTitle = \"\"\"$" + escaped + "\"\"\"",
		"val price = \"$`\"",
	}
	for _, declaration := range source {
		const comment = "/* ordinary note */"
		content := []byte(declaration + "\n" + comment + "\n")
		want := declaration + "\n" + strings.Repeat(" ", len(comment)) + "\n"
		if got := string(StripBlockCommentsForFile(content, "Example.kt")); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestStripBlockCommentsForFileHonorsLanguageCommentNesting(t *testing.T) {
	const note = "/* opening /* inner note */"
	const kotlinTail = "  keep the reader's note inside */"
	kotlinSource := note + "\r\n" + kotlinTail + "\r\nval next = 2"
	kotlinWant := strings.Repeat(" ", len(note)) + "\r\n" +
		strings.Repeat(" ", len(kotlinTail)) + "\r\nval next = 2"
	if got := string(StripBlockCommentsForFile([]byte(kotlinSource), "Example.kt")); got != kotlinWant {
		t.Errorf("got Kotlin source %q, want %q", got, kotlinWant)
	}
	const javaTail = " int next = 2;"
	javaWant := strings.Repeat(" ", len(note)) + javaTail
	if got := string(StripBlockCommentsForFile([]byte(note+javaTail), "Example.java")); got != javaWant {
		t.Errorf("got Java source %q, want %q", got, javaWant)
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
	const unfinishedJavaString = `"label \" quoted`
	const unfinishedJavaTextBlock = "\"\"\"\nlabel"
	tests := []struct {
		name     string
		filePath string
		content  string
		want     string
	}{
		{name: "line comment", content: "// note", want: "// note"},
		{name: "unfinished block comment", content: unfinishedBlock, want: strings.Repeat(" ", len(unfinishedBlock))},
		{name: "unfinished quoted string", content: `"note`, want: `"note`},
		{name: "unfinished raw string", content: `"""note`, want: `"""note`},
		{name: "unfinished Java string", filePath: "Example.java", content: unfinishedJavaString, want: unfinishedJavaString},
		{name: "unfinished Java text block", filePath: "Example.java", content: unfinishedJavaTextBlock, want: unfinishedJavaTextBlock},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := []byte(test.content)
			if got := string(StripBlockCommentsForFile(input, test.filePath)); got != test.want {
				t.Errorf("got %q, want %q", got, test.want)
			}
			if string(input) != test.content {
				t.Error("changed input bytes")
			}
		})
	}
}
