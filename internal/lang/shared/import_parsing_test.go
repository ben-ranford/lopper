package shared

import "testing"

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
	if got := StripLineComment("import a # trailing", "#"); got != "import a " {
		t.Fatalf("unexpected stripped value %q", got)
	}
	location := Location("app.py", 4, 6)
	if location.File != "app.py" || location.Line != 4 || location.Column != 6 {
		t.Fatalf("unexpected location: %+v", location)
	}
	lineLocation := LocationFromLine("app.py", 1, "  import b")
	if lineLocation.Line != 2 || lineLocation.Column != 3 {
		t.Fatalf("unexpected line location: %+v", lineLocation)
	}
}
