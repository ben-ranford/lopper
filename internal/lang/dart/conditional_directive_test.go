package dart

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestParseImportDirectiveAcceptsConditionalClauses(t *testing.T) {
	testCases := map[string]struct {
		directive string
		kind      string
		module    string
		clause    string
	}{
		"one branch": {
			directive: `import 'package:http/http.dart' if (dart.library.io) 'src/io.dart';`,
			kind:      "import",
			module:    "package:http/http.dart",
			clause:    "if (dart.library.io) 'src/io.dart'",
		},
		"two branches": {
			directive: `import 'package:http/http.dart' if (dart.library.io) 'src/io.dart' if (dart.library.html) 'src/html.dart';`,
			kind:      "import",
			module:    "package:http/http.dart",
			clause:    "if (dart.library.io) 'src/io.dart' if (dart.library.html) 'src/html.dart'",
		},
		"branch then alias": {
			directive: `import 'package:http/http.dart' if (dart.library.io) 'src/io.dart' as http;`,
			kind:      "import",
			module:    "package:http/http.dart",
			clause:    "if (dart.library.io) 'src/io.dart' as http",
		},
		"branch then show": {
			directive: `import 'package:http/http.dart' if (dart.library.io) 'src/io.dart' show Client, Request;`,
			kind:      "import",
			module:    "package:http/http.dart",
			clause:    "if (dart.library.io) 'src/io.dart' show Client, Request",
		},
		"equality test": {
			directive: `import 'package:foo/foo.dart' if (dart.library.io == 'true') 'src/io.dart';`,
			kind:      "import",
			module:    "package:foo/foo.dart",
			clause:    "if (dart.library.io == 'true') 'src/io.dart'",
		},
		"equality test double quoted": {
			directive: `export 'src/api.dart' if (dart.library.io == "true") "src/api_io.dart";`,
			kind:      "export",
			module:    "src/api.dart",
			clause:    `if (dart.library.io == "true") "src/api_io.dart"`,
		},
		"equality test with alias": {
			directive: `import 'package:foo/foo.dart' if (dart.library.html=='true') 'src/html.dart' as foo;`,
			kind:      "import",
			module:    "package:foo/foo.dart",
			clause:    "if (dart.library.html=='true') 'src/html.dart' as foo",
		},
		"raw string fallback": {
			directive: `import 'package:http/http.dart' if (dart.library.io) r'src/io.dart';`,
			kind:      "import",
			module:    "package:http/http.dart",
			clause:    "if (dart.library.io) r'src/io.dart'",
		},
		"export": {
			directive: `export 'src/api.dart' if (dart.library.html) 'src/api_html.dart';`,
			kind:      "export",
			module:    "src/api.dart",
			clause:    "if (dart.library.html) 'src/api_html.dart'",
		},
		"indented continuation": {
			directive: "import 'package:http/http.dart'\n    if (dart.library.io) 'src/io.dart'\n    as http;",
			kind:      "import",
			module:    "package:http/http.dart",
			clause:    "if (dart.library.io) 'src/io.dart'\n    as http",
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			kind, module, clause, ok := parseImportDirective(testCase.directive)
			if !ok {
				t.Fatalf("expected %q to parse, got ok=false", testCase.directive)
			}
			if kind != testCase.kind || module != testCase.module || clause != testCase.clause {
				t.Fatalf("parseImportDirective(%q) = (%q, %q, %q), want (%q, %q, %q)",
					testCase.directive, kind, module, clause, testCase.kind, testCase.module, testCase.clause)
			}
		})
	}
}

func TestParseImportDirectiveRejectsMalformedConditionalClauses(t *testing.T) {
	testCases := map[string]string{
		"branch without uri":          `import 'package:http/http.dart' if (dart.library.io);`,
		"branch without parens":       `import 'package:http/http.dart' if dart.library.io 'src/io.dart';`,
		"empty condition":             `import 'package:http/http.dart' if () 'src/io.dart';`,
		"unquoted fallback":           `import 'package:http/http.dart' if (dart.library.io) src/io.dart;`,
		"unknown clause after if":     `import 'package:http/http.dart' if (dart.library.io) 'src/io.dart' unless x;`,
		"conditional before primary":  `import if (dart.library.io) 'src/io.dart' 'package:http/http.dart';`,
		"empty fallback uri":          `import 'package:http/http.dart' if (dart.library.io) '';`,
		"empty condition segment":     `import 'package:http/http.dart' if (dart..io) 'src/io.dart';`,
		"trailing dot condition":      `import 'package:http/http.dart' if (dart.library.) 'src/io.dart';`,
		"branch after combinator":     `import 'package:http/http.dart' show Client if (dart.library.io) 'src/io.dart' as x unless y;`,
		"equality without operand":    `import 'package:foo/foo.dart' if (dart.library.io ==) 'src/io.dart';`,
		"unquoted equality operand":   `import 'package:foo/foo.dart' if (dart.library.io == true) 'src/io.dart';`,
		"equality on empty condition": `import 'package:foo/foo.dart' if (== 'true') 'src/io.dart';`,
	}

	for name, directive := range testCases {
		t.Run(name, func(t *testing.T) {
			if kind, module, clause, ok := parseImportDirective(directive); ok {
				t.Fatalf("expected %q to be rejected, got (%q, %q, %q)", directive, kind, module, clause)
			}
		})
	}
}

func TestDartAdapterReportsConditionalPackageImport(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), appHTTPManifest)
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), `import 'package:http/http.dart'
    if (dart.library.io) 'src/io.dart'
    if (dart.library.html) 'src/html.dart'
    as http;

void main() {
  final client = http.Client();
  client.close();
}
`)

	depReport, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "http",
	})
	if err != nil {
		t.Fatalf("analyse conditional import dependency: %v", err)
	}
	if len(depReport.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(depReport.Dependencies))
	}
	if depReport.Dependencies[0].UsedExportsCount == 0 {
		t.Fatalf("expected a conditional import's alias usage to be detected, got %#v", depReport.Dependencies[0])
	}
}

func TestBuildDirectiveBindingsIgnoresIdentifiersInConditionalUris(t *testing.T) {
	testCases := map[string]struct {
		directive string
		want      []string
	}{
		"alias only inside a fallback uri": {
			directive: `import 'package:http/http.dart' if (dart.library.io) 'src/as io.dart' as http;`,
			want:      []string{"http"},
		},
		"show only inside a fallback uri": {
			directive: `import 'package:http/http.dart' if (dart.library.io) 'src/show Client.dart';`,
			want:      []string{"*"},
		},
		"unicode escape in a fallback uri": {
			directive: `import 'package:http/http.dart' if (dart.library.io) 'src/mobile\u0020platform.dart' as platform;`,
			want:      []string{"platform"},
		},
		"show after the branch": {
			directive: `import 'package:http/http.dart' if (dart.library.io) 'src/io.dart' show Client, Request;`,
			want:      []string{"Client", "Request"},
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			kind, module, clause, ok := parseImportDirective(testCase.directive)
			if !ok {
				t.Fatalf("expected %q to parse", testCase.directive)
			}
			bindings := buildDirectiveBindings(kind, module, clause, "http", report.Location{})
			got := make([]string, 0, len(bindings))
			for _, binding := range bindings {
				got = append(got, binding.Name)
			}
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("bindings for %q = %v, want %v", testCase.directive, got, testCase.want)
			}
		})
	}
}
