package python

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

func BenchmarkBuildUnusedImportCodemodReport(b *testing.B) {
	repo := b.TempDir()
	const fileCount = 100
	files := make([]fileScan, 0, fileCount)
	for index := range fileCount {
		path := filepath.Join("pkg", fmt.Sprintf("file-%03d.py", index))
		absolutePath := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
			b.Fatalf("create benchmark fixture directory: %v", err)
		}
		if err := os.WriteFile(absolutePath, []byte("import requests\n"), 0o644); err != nil {
			b.Fatalf("write benchmark fixture: %v", err)
		}
		files = append(files, fileScan{
			Path: path,
			Imports: []importBinding{{
				Dependency: "requests",
				Module:     "requests",
				Name:       "requests",
				Local:      "requests",
				Location:   report.Location{File: path, Line: 1},
			}},
			Usage: map[string]int{},
		})
	}
	scan := scanResult{Files: files}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		codemodReport, warnings := BuildUnusedImportCodemodReport(repo, "requests", scan)
		if len(warnings) != 0 || len(codemodReport.Suggestions) != fileCount {
			b.Fatalf("unexpected benchmark result: suggestions=%d warnings=%#v", len(codemodReport.Suggestions), warnings)
		}
	}
}

func TestPythonCodemodBuildBranches(t *testing.T) {
	repo := t.TempDir()
	missingFile := fileScan{
		Path: "missing.py",
		Imports: []importBinding{{
			Dependency: "requests",
			Module:     "requests",
			Name:       "requests",
			Local:      "requests",
			Location:   report.Location{File: "missing.py", Line: 1},
		}},
		Usage: map[string]int{},
	}
	suggestions, skips, warnings := buildPythonCodemodForFile(repo, "requests", missingFile, map[string][]string{})
	if len(suggestions) != 0 || len(skips) != 0 || len(warnings) != 1 {
		t.Fatalf("expected missing source warning only, suggestions=%#v skips=%#v warnings=%#v", suggestions, skips, warnings)
	}

	outOfRange := missingFile
	outOfRange.Path = "main.py"
	suggestions, skips, warnings = buildPythonCodemodForFile(repo, "requests", outOfRange, map[string][]string{"main.py": {}})
	if len(suggestions) != 0 || len(warnings) != 0 || len(skips) != 1 || skips[0].ReasonCode != pythonCodemodReasonSourceLineUnavailable {
		t.Fatalf("expected source-line-unavailable skip, suggestions=%#v skips=%#v warnings=%#v", suggestions, skips, warnings)
	}
}

func TestPythonCodemodUnsafeReasonBranches(t *testing.T) {
	repo := t.TempDir()
	target := []importBinding{
		{Dependency: "requests", Module: "requests", Name: "get", Local: "get"},
		{Dependency: "requests", Module: "requests", Name: "post", Local: "post"},
	}

	cases := []struct {
		name          string
		file          string
		line          string
		lineImports   []importBinding
		targetImports []importBinding
		unusedImports []importBinding
		want          string
	}{
		{name: "public api", file: "__init__.py", line: "import requests", lineImports: target[:1], targetImports: target[:1], unusedImports: target[:1], want: pythonCodemodReasonPublicAPIFile},
		{name: "empty", file: "main.py", line: "   ", lineImports: target[:1], targetImports: target[:1], unusedImports: target[:1], want: pythonCodemodReasonUnsupportedSyntax},
		{name: "compound", file: "main.py", line: "import requests; print('x')", lineImports: target[:1], targetImports: target[:1], unusedImports: target[:1], want: pythonCodemodReasonUnsupportedSyntax},
		{name: "continued", file: "main.py", line: "import requests \\", lineImports: target[:1], targetImports: target[:1], unusedImports: target[:1], want: pythonCodemodReasonUnsupportedSyntax},
		{name: "parenthesized", file: "main.py", line: "from requests import (get)", lineImports: target[:1], targetImports: target[:1], unusedImports: target[:1], want: pythonCodemodReasonUnsupportedSyntax},
		{name: "mixed used", file: "main.py", line: "from requests import get, post", lineImports: target, targetImports: target, unusedImports: target[:1], want: pythonCodemodReasonMixedUsedLine},
		{name: "mixed dependency", file: "main.py", line: "import requests, os", lineImports: target[:1], targetImports: target[:1], unusedImports: target[:1], want: pythonCodemodReasonMixedDependencyLine},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, message := pythonUnsafeUnusedImportLineReason(repo, "requests", tc.file, tc.line, tc.lineImports, tc.targetImports, tc.unusedImports)
			if got != tc.want || strings.TrimSpace(message) == "" {
				t.Fatalf("expected reason %q with message, got reason=%q message=%q", tc.want, got, message)
			}
		})
	}

	if reason, message := pythonUnsafeUnusedImportLineReason(repo, "requests", "main.py", "from requests import get, post", target, target, target); reason != "" || message != "" {
		t.Fatalf("expected safe line to have no unsafe reason, got reason=%q message=%q", reason, message)
	}
}

func TestPythonCodemodLineMatchingHelpers(t *testing.T) {
	repo := t.TempDir()
	requestsImport := []importBinding{{Dependency: "requests", Module: "requests", Name: "requests", Local: "requests"}}
	requestsFrom := []importBinding{{Dependency: "requests", Module: "requests", Name: "get", Local: "get"}}

	if !pythonLineTextMatchesParsedImports(repo, "requests", "import requests", requestsImport) {
		t.Fatal("expected import line to match parsed imports")
	}
	if !pythonLineTextMatchesParsedImports(repo, "requests", "from requests import get", requestsFrom) {
		t.Fatal("expected from-import line to match parsed imports")
	}
	if pythonLineTextMatchesParsedImports(repo, "requests", "import requests, os", requestsImport) {
		t.Fatal("expected mixed raw import line to fail parsed import matching")
	}
	if pythonLineTextMatchesParsedImports(repo, "requests", "print('requests')", requestsImport) {
		t.Fatal("expected non-import line to fail parsed import matching")
	}
	if pythonImportPartsTargetDependency(repo, "requests", nil) {
		t.Fatal("expected empty import parts to fail target dependency matching")
	}
	if pythonImportPartsTargetDependency(repo, "requests", []string{"os"}) {
		t.Fatal("expected stdlib import part to fail target dependency matching")
	}
	if _, ok := pythonSourceLine([]string{"one"}, 0); ok {
		t.Fatal("expected line zero to be unavailable")
	}
	if _, ok := pythonSourceLine([]string{"one"}, 2); ok {
		t.Fatal("expected out-of-range line to be unavailable")
	}
}

func TestPythonCodemodNormalizesParenthesizedFromImportToOpeningLine(t *testing.T) {
	lines := []string{
		"value = f\"{f'''{\"\"\"",
		"",
		"import requests",
		"\"\"\"}'''}\"",
		"from numpy import (",
		"    array,",
		")",
	}
	importsByLine := pythonImportsByLine(lines, []importBinding{{
		Dependency: "numpy",
		Module:     "numpy",
		Name:       "array",
		Local:      "array",
		Location:   report.Location{File: "main.py", Line: 6},
	}})
	if len(importsByLine) != 1 || len(importsByLine[5]) != 1 {
		t.Fatalf("expected multiline from-import binding to group on opening line 5, got %#v", importsByLine)
	}

	simpleImports := pythonImportsByLine([]string{"import requests, os"}, []importBinding{
		{Dependency: "requests", Module: "requests", Name: "requests", Local: "requests", Location: report.Location{File: "main.py", Line: 1}},
		{Dependency: "os", Module: "os", Name: "os", Local: "os", Location: report.Location{File: "main.py", Line: 1}},
	})
	if len(simpleImports) != 1 || len(simpleImports[1]) != 2 {
		t.Fatalf("expected simple imports to stay on line 1, got %#v", simpleImports)
	}
}

// TestPythonCodemodOpenerScanIgnoresImportLikeStringContent proves
// pythonFromImportOpenerLines masks string content the same way parseImports
// does before matching "from X import (" opener lines: an unbalanced,
// import-like example inside a docstring must not be mistaken for a real
// opener that swallows an unrelated later import as its "continuation".
func TestPythonCodemodOpenerScanIgnoresImportLikeStringContent(t *testing.T) {
	lines := []string{
		`"""`,
		"from bogus import (",
		`"""`,
		"from real import (",
		"    get,",
		")",
	}
	importsByLine := pythonImportsByLine(lines, []importBinding{{
		Dependency: "real",
		Module:     "real",
		Name:       "get",
		Local:      "get",
		Location:   report.Location{File: "main.py", Line: 5},
	}})
	if len(importsByLine) != 1 || len(importsByLine[4]) != 1 {
		t.Fatalf("expected the real import to group on opening line 4, not the docstring's bogus opener, got %#v", importsByLine)
	}
}

// TestPythonCodemodGroupsLargeParenthesizedFromImportWithoutQuadraticScan
// proves the fix for a reported quadratic slowdown: pythonImportStatementLine
// used to backward-scan from every imported symbol's own line all the way to
// the block's opener, making grouping O(n^2) in the number of symbols for a
// single large parenthesized from-import. pythonFromImportOpenerLines now
// resolves every symbol's opener in one forward pass instead. The generous
// wall-clock bound below is loose enough to avoid flaking on a slow CI
// runner, but tight enough that the old quadratic backward scan -- which at
// this symbol count performs on the order of symbolCount^2/2 regex matches
// -- would blow through it by orders of magnitude.
func TestPythonCodemodGroupsLargeParenthesizedFromImportWithoutQuadraticScan(t *testing.T) {
	const symbolCount = 4000
	lines := make([]string, 0, symbolCount+2)
	lines = append(lines, "from numpy import (")
	imports := make([]importBinding, 0, symbolCount)
	for i := range symbolCount {
		name := fmt.Sprintf("symbol_%d", i)
		lines = append(lines, "    "+name+",")
		imports = append(imports, importBinding{
			Dependency: "numpy",
			Module:     "numpy",
			Name:       name,
			Local:      name,
			Location:   report.Location{File: "main.py", Line: i + 2},
		})
	}
	lines = append(lines, ")")

	start := time.Now()
	importsByLine := pythonImportsByLine(lines, imports)
	elapsed := time.Since(start)

	if len(importsByLine) != 1 || len(importsByLine[1]) != symbolCount {
		t.Fatalf("expected all %d symbols to group on opening line 1, got %d groups with %d on line 1", symbolCount, len(importsByLine), len(importsByLine[1]))
	}
	const budget = 2 * time.Second
	if elapsed > budget {
		t.Fatalf("grouping %d symbols took %s, exceeding the %s budget for a linear scan", symbolCount, elapsed, budget)
	}
}

func TestPythonCodemodSmallHelpers(t *testing.T) {
	imports := []importBinding{
		{Module: "requests", Name: "get", Local: "get"},
		{Module: "requests", Name: "post", Local: "post"},
		{Module: "", Name: "ignored", Local: "ignored"},
	}
	if got := pythonLineModule(imports); got != "requests" {
		t.Fatalf("expected deduped module, got %q", got)
	}
}
