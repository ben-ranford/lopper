package shared

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCodemodSuggestionAndSkipBuilders(t *testing.T) {
	suggestion := NewCodemodSuggestion(CodemodSuggestionSpec{
		Language:          "python",
		Dependency:        "requests",
		File:              "main.py",
		Line:              1,
		ImportName:        "requests",
		FromModule:        "requests",
		Original:          "import requests",
		Replacement:       "",
		Patch:             BuildDeleteLinePatch("main.py", 1, "import requests"),
		SafetyReasonCodes: []string{" all-imports-unused ", "", "all-imports-unused", "source-line-match"},
		DeleteLine:        true,
	})
	if suggestion.TargetFile != "main.py" || !suggestion.DeleteLine {
		t.Fatalf("expected target-file default and delete marker, got %#v", suggestion)
	}
	if !reflect.DeepEqual(suggestion.SafetyReasonCodes, []string{"all-imports-unused", "source-line-match"}) {
		t.Fatalf("expected cleaned safety reason codes, got %#v", suggestion.SafetyReasonCodes)
	}
	if !strings.Contains(suggestion.Patch, "@@ -1,1 +0,0 @@") || !strings.Contains(suggestion.Patch, "-import requests") {
		t.Fatalf("unexpected delete patch: %q", suggestion.Patch)
	}

	replacementPatch := BuildSingleLinePatch("src/index.ts", 2, "old", "new")
	if !strings.Contains(replacementPatch, "@@ -2 +2 @@") || !strings.Contains(replacementPatch, "+new") {
		t.Fatalf("unexpected replacement patch: %q", replacementPatch)
	}

	skip := NewCodemodSkip(CodemodSkipSpec{
		Language:   "python",
		Dependency: "requests",
		File:       "main.py",
		Line:       2,
		ImportName: "Session",
		Module:     "requests",
		ReasonCode: "mixed-used-import-line",
		Message:    "line mixes used and unused imports",
	})
	if skip.TargetFile != "main.py" || skip.Dependency != "requests" || skip.ReasonCode != "mixed-used-import-line" {
		t.Fatalf("unexpected skip payload: %#v", skip)
	}
}

func TestLoadCodemodSourceLines(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "main.py"), []byte("import requests\r\nprint('ok')\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cache := make(map[string][]string)
	lines, warning, loaded := LoadCodemodSourceLines(repo, "main.py", cache)
	if !loaded || warning != "" || !reflect.DeepEqual(lines[:2], []string{"import requests", "print('ok')"}) {
		t.Fatalf("unexpected loaded lines: lines=%#v warning=%q loaded=%t", lines, warning, loaded)
	}

	cache["main.py"] = []string{"cached"}
	lines, warning, loaded = LoadCodemodSourceLines(repo, "main.py", cache)
	if !loaded || warning != "" || !reflect.DeepEqual(lines, []string{"cached"}) {
		t.Fatalf("expected cached source lines, lines=%#v warning=%q loaded=%t", lines, warning, loaded)
	}

	_, warning, loaded = LoadCodemodSourceLines(repo, "missing.py", cache)
	if loaded || !strings.Contains(warning, "codemod preview skipped for missing.py") {
		t.Fatalf("expected missing-file warning, warning=%q loaded=%t", warning, loaded)
	}
}

func TestCodemodPatchHeadersEscapeFilenameBytes(t *testing.T) {
	const filename = "space \"\\\t\r\n\x01\x7f\xc3\xa9\xff.js"
	const encoded = `space \"\\\011\015\012\001\177\303\251\377.js`
	header := "--- \"a/" + encoded + "\"\n+++ \"b/" + encoded + "\"\n"
	for _, patch := range []string{
		BuildSingleLinePatch(filename, 1, "old", "new"),
		BuildDeleteLinePatch(filename, 1, "old"),
	} {
		if !strings.HasPrefix(patch, header) {
			t.Fatalf("filename bytes leaked into patch structure: %q", patch)
		}
	}
}
