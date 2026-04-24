package swift

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftManifestParserIgnoresCommentedPackagesAndInlineApostrophes(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), `import PackageDescription
let package = Package(
  name: "Demo",
  dependencies: [
    // .package(url: "https://github.com/commented/inline.git", from: "9.9.9"),
    .package(url: "https://github.com/apple/swift-nio.git", from: "2.0.0"), // don't break on apostrophes
    /*
     .package(url: "https://github.com/commented/block.git", from: "9.9.9")
    */
  ],
  targets: [
    .target(name: "Demo")
  ]
)`)

	catalog := newTestSwiftCatalog()
	found, warnings, err := loadManifestData(repo, &catalog)
	if err != nil || !found {
		t.Fatalf("expected manifest loader success, found=%v err=%v", found, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for manifest with active package declarations, got %#v", warnings)
	}
	if len(catalog.Dependencies) != 1 {
		t.Fatalf("expected only active dependency to be discovered, got %#v", catalog.Dependencies)
	}
	if _, ok := catalog.Dependencies["swift-nio"]; !ok {
		t.Fatalf("expected swift-nio to be discovered, got %#v", catalog.Dependencies)
	}
	for _, depID := range []string{"inline", "block"} {
		if _, ok := catalog.Dependencies[depID]; ok {
			t.Fatalf("expected commented dependency %q to be ignored, got %#v", depID, catalog.Dependencies)
		}
	}
}

func TestSwiftCarthageParserPreservesHashFragmentsInQuotedSource(t *testing.T) {
	entry, ok := parseCarthageLine(`binary "https://example.com/FancyKit.json#sha256=abc123" "1.2.3" # trailing comment`, true)
	if !ok {
		t.Fatalf("expected Carthage line with quoted hash fragment to parse")
	}
	if entry.Source != "https://example.com/FancyKit.json#sha256=abc123" {
		t.Fatalf("expected hash fragment in source to be preserved, got %#v", entry)
	}
	if entry.Dependency != "fancykit" {
		t.Fatalf("expected dependency to derive from binary source path, got %#v", entry)
	}
}

func TestSwiftParseImportsIgnoresBlockCommentedImports(t *testing.T) {
	content := []byte(`/* import HiddenKit */
/*
import NestedHidden
/* import AlsoHidden */
*/
import Alamofire
`)
	imports := parseSwiftImports(content, swiftMainFileName)
	modules := make([]string, 0, len(imports))
	for _, imported := range imports {
		modules = append(modules, imported.Module)
	}
	if !slices.Equal(modules, []string{"Alamofire"}) {
		t.Fatalf("expected only active imports outside block comments, got %#v", modules)
	}
}

func TestStripHashCommentOutsideQuotes(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{line: `github "owner/repo" "1.0.0" # trailing`, want: `github "owner/repo" "1.0.0" `},
		{line: `binary "https://example.com/FancyKit.json#sha=abc" "1.2.3"`, want: `binary "https://example.com/FancyKit.json#sha=abc" "1.2.3"`},
		{line: `binary "https://example.com/FancyKit.json#sha=abc\"def" "1.2.3" # trailing`, want: `binary "https://example.com/FancyKit.json#sha=abc\"def" "1.2.3" `},
		{line: `git "https://example.com/kit.git" "main"`, want: `git "https://example.com/kit.git" "main"`},
	}

	for _, tc := range cases {
		if got := stripHashCommentOutsideQuotes(tc.line); got != tc.want {
			t.Fatalf("unexpected hash comment stripping for %q: got %q want %q", tc.line, got, tc.want)
		}
	}
}

func TestSwiftCommentMaskPreservesStringContent(t *testing.T) {
	masked := blankSwiftCommentsPreservingStrings(`let escaped = "value \"quoted\" # keep"
let multiline = """
keep this line
"""
// strip this comment
/* strip this block */
let done = true
`)
	if !strings.Contains(masked, `"value \"quoted\" # keep"`) {
		t.Fatalf("expected escaped quoted string to be preserved, got %q", masked)
	}
	if !strings.Contains(masked, "keep this line") {
		t.Fatalf("expected multiline string body to be preserved, got %q", masked)
	}
	if strings.Contains(masked, "strip this comment") || strings.Contains(masked, "strip this block") {
		t.Fatalf("expected comments to be stripped while preserving strings, got %q", masked)
	}
	if !strings.Contains(masked, "let done = true") {
		t.Fatalf("expected non-comment code to remain visible, got %q", masked)
	}
}
