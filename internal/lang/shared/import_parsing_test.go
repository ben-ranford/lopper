package shared

import (
	"strings"
	"testing"
)

func TestParseImportLines(t *testing.T) {
	content := []byte("import a\n  import b // note\n")
	imports := ParseImportLines(content, "main.py", func(line string, _ int) []ImportRecord {
		line = StripLineComment(line, "//")
		switch line {
		case "import a":
			return []ImportRecord{{Dependency: "dep-a", Module: "a", Name: "a", Local: "a"}}
		case "  import b ":
			return []ImportRecord{{Dependency: "dep-b", Module: "b", Name: "b", Local: "b"}}
		default:
			return nil
		}
	})

	if len(imports) != 2 {
		t.Fatalf("expected 2 imports, got %d", len(imports))
	}
	if imports[0].Location.Line != 1 || imports[0].Location.Column != 1 {
		t.Fatalf("unexpected first location: %+v", imports[0].Location)
	}
	if imports[1].Location.Line != 2 || imports[1].Location.Column != 3 {
		t.Fatalf("unexpected second location: %+v", imports[1].Location)
	}
}

func TestStripLineCommentAndLocationHelpers(t *testing.T) {
	const appFile = "app.py"

	if got := StripLineComment("import a # trailing", "#"); got != "import a " {
		t.Fatalf("unexpected stripped value %q", got)
	}
	location := Location(appFile, 4, 6)
	if location.File != appFile || location.Line != 4 || location.Column != 6 {
		t.Fatalf("unexpected location: %+v", location)
	}
	if got := FirstContentColumn("  import b"); got != 3 {
		t.Fatalf("unexpected first content column: %d", got)
	}
	locationAtLineTwo := LocationFromLine(appFile, 1, "  import b")
	if locationAtLineTwo.Line != 2 || locationAtLineTwo.Column != 3 {
		t.Fatalf("unexpected line location: %+v", locationAtLineTwo)
	}
}

func TestStripBlockComments(t *testing.T) {
	if stripped := StripBlockComments(nil); len(stripped) != 0 {
		t.Fatalf("expected nil block-comment input to stay nil, got %#v", stripped)
	}

	content := []byte("import a\n/* comment\nimport b\n*/\nimport c /* tail */\n")

	stripped := StripBlockComments(content)
	lines := strings.Split(string(stripped), "\n")
	if len(lines) != 6 {
		t.Fatalf("expected 6 lines after stripping, got %#v", lines)
	}
	if lines[0] != "import a" {
		t.Fatalf("expected first line to remain unchanged, got %q", lines[0])
	}
	if strings.TrimSpace(lines[1]) != "" || strings.TrimSpace(lines[2]) != "" || strings.TrimSpace(lines[3]) != "" {
		t.Fatalf("expected block-comment lines to be blanked, got %#v", lines[1:4])
	}
	if !strings.HasPrefix(lines[4], "import c") {
		t.Fatalf("expected trailing inline block comment to preserve import line, got %q", lines[4])
	}
}
