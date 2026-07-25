package runtime

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeRuntimeContextLabelReturnsEmptyForURLs(t *testing.T) {
	if got := normalizeRuntimeContextLabel("https://example.com/app.js"); got != "" {
		t.Fatalf("expected URL label to be redacted, got %q", got)
	}
}

func TestNormalizeRuntimeContextLabelReturnsEmptyForRejectedSchemes(t *testing.T) {
	for _, value := range []string{
		"x:private-token",
		"a:foo/bar.js",
		"C:Users/alice/private.js",
		"data:text/plain,secret",
		"mailto:test@example.com",
		"https:foo",
		"https:/foo",
	} {
		if got := normalizeRuntimeContextLabel(value); got != "" {
			t.Fatalf("expected rejected scheme label %q to be redacted, got %q", value, got)
		}
	}
}

func TestNormalizeRuntimeContextLabelReturnsEmptyForBackslashValues(t *testing.T) {
	if got := normalizeRuntimeContextLabel(`src\main.js`); got != "" {
		t.Fatalf("expected backslash label to be redacted, got %q", got)
	}
}

func TestNormalizeRuntimeContextLabelReturnsEmptyForAbsolutePaths(t *testing.T) {
	if got := normalizeRuntimeContextLabel("/tmp/app.js"); got != "" {
		t.Fatalf("expected absolute label to be redacted, got %q", got)
	}
}

func TestNormalizeRuntimeContextLabelReturnsEmptyForDotRelativePaths(t *testing.T) {
	if got := normalizeRuntimeContextLabel("../src/app.js"); got != "" {
		t.Fatalf("expected dot-relative label to be redacted, got %q", got)
	}
}

func TestLooksLikeFilesystemPathReturnsTrueForDotRelativePaths(t *testing.T) {
	if !looksLikeFilesystemPath("./src/app.js") {
		t.Fatalf("expected dot-relative path to be treated as filesystem path")
	}
}

func TestLooksLikeFilesystemPathReturnsTrueForWindowsDrivePaths(t *testing.T) {
	if !looksLikeFilesystemPath(`C:\repo\app.js`) {
		t.Fatalf("expected Windows drive path to be treated as filesystem path")
	}
}

func TestLooksLikeFilesystemPathReturnsTrueForHiddenBasePaths(t *testing.T) {
	if !looksLikeFilesystemPath("config/.env") {
		t.Fatalf("expected hidden base path to be treated as filesystem path")
	}
}

func TestPathLikeExtensionReturnsEmptyForNonAlphanumericSuffix(t *testing.T) {
	if got := pathLikeExtension("trace.j-s"); got != "" {
		t.Fatalf("expected non-alphanumeric extension to be rejected, got %q", got)
	}
}

func TestResolveRuntimeContextPathReturnsEmptyForBlankInput(t *testing.T) {
	if got := resolveRuntimeContextPath("   "); got != "" {
		t.Fatalf("expected blank path to normalize empty, got %q", got)
	}
}

func TestRuntimeContextRepoRelativeReturnsDotForRepoRoot(t *testing.T) {
	rel, ok := runtimeContextRepoRelative("/repo", "/repo")
	if !ok {
		t.Fatalf("expected repo root to resolve relative path")
	}
	if rel != "." {
		t.Fatalf("expected repo root relative path '.', got %q", rel)
	}
}

func TestNormalizeRuntimeContextValueParsesFileURLs(t *testing.T) {
	repo := t.TempDir()
	modulePath := filepath.Join(repo, "src", "hello world.js")
	if err := os.MkdirAll(filepath.Dir(modulePath), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(modulePath, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write repo module: %v", err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(outsidePath, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write outside module: %v", err)
	}

	repoURL := fileURLForRuntimeTest(repo, "")
	opts := traceLoadOptions{repoRoot: repo, resolvedRepoRoot: resolvedRuntimeRepoRoot(repo)}
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "local unix path", value: fileURLForRuntimeTest(modulePath, ""), want: "src/hello world.js"},
		{name: "localhost path", value: fileURLForRuntimeTest(modulePath, "LOCALHOST"), want: "src/hello world.js"},
		{name: "outside unix path", value: fileURLForRuntimeTest(outsidePath, ""), want: ""},
		{name: "remote host", value: "file://server/share/project/main.js", want: ""},
		{name: "localhost windows drive", value: "file://localhost/C:/Users/alice/project/main.js", want: ""},
		{name: "encoded windows drive", value: "file://localhost/%43%3A%2FUsers/alice/project/main.js", want: ""},
		{name: "encoded UNC path", value: "file://localhost/%2F%2Fserver/share/project/main.js", want: ""},
		{name: "empty file URL path", value: "file://localhost", want: ""},
		{name: "reject one-letter pseudo-scheme label", value: "x:private-token", want: ""},
		{name: "reject one-letter pseudo-scheme path", value: "a:foo/bar.js", want: ""},
		{name: "reject drive-relative path", value: "C:Users/alice/private.js", want: ""},
		{name: "rejected data scheme", value: "data:text/plain,secret", want: ""},
		{name: "rejected mailto scheme", value: "mailto:test@example.com", want: ""},
		{name: "rejected https opaque", value: "https:foo", want: ""},
		{name: "rejected https single slash", value: "https:/foo", want: ""},
		{name: "encoded traversal", value: strings.TrimSuffix(repoURL, "/") + "/src%2F..%2F..%2FUsers/alice/main.js", want: ""},
		{name: "malformed escape", value: strings.TrimSuffix(repoURL, "/") + "/src/bad%ZZ.js", want: ""},
		{name: "node package label", value: "node:internal/modules/cjs/loader", want: "node:internal/modules/cjs/loader"},
		{name: "package subpath label", value: "lodash/map", want: "lodash/map"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeRuntimeContextValue(tc.value, opts); got != tc.want {
				t.Fatalf("normalize runtime context %q: got %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

func TestRuntimeContextSchemeOnlyExemptsAbsoluteWindowsDrives(t *testing.T) {
	tests := []struct {
		value    string
		want     string
		wantOkay bool
	}{
		{value: `C:\Users\alice\main.js`, want: "", wantOkay: false},
		{value: "D:/repo/main.js", want: "", wantOkay: false},
		{value: "x:private-token", want: "x", wantOkay: true},
		{value: "a:foo/bar.js", want: "a", wantOkay: true},
		{value: "C:Users/alice/private.js", want: "C", wantOkay: true},
		{value: "node:internal/modules/cjs/loader", want: "node", wantOkay: true},
		{value: "x", want: "", wantOkay: false},
		{value: "bad_scheme:value", want: "", wantOkay: false},
		{value: "1:/private.js", want: "", wantOkay: false},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			got, ok := runtimeContextScheme(tc.value)
			if got != tc.want || ok != tc.wantOkay {
				t.Fatalf("runtimeContextScheme(%q) = (%q, %t), want (%q, %t)", tc.value, got, ok, tc.want, tc.wantOkay)
			}
		})
	}
}

func TestNormalizeRuntimeContextPathResolvesRepoRootOnDemand(t *testing.T) {
	repo := t.TempDir()
	modulePath := filepath.Join(repo, "src", "main.js")
	if err := os.MkdirAll(filepath.Dir(modulePath), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(modulePath, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write repo module: %v", err)
	}

	got, ok := normalizeRuntimeContextPath("src/main.js", traceLoadOptions{repoRoot: repo})
	if !ok || got != "src/main.js" {
		t.Fatalf("normalize context with lazy repo root = (%q, %t), want (%q, true)", got, ok, "src/main.js")
	}
}

func TestNormalizeRuntimeContextPathRejectsUnresolvableRepoRoot(t *testing.T) {
	original := resolveTraceRepoRoot
	resolveTraceRepoRoot = func(string) string {
		return ""
	}
	t.Cleanup(func() {
		resolveTraceRepoRoot = original
	})

	got, ok := normalizeRuntimeContextPath("src/main.js", traceLoadOptions{repoRoot: "/repo"})
	if !ok || got != "" {
		t.Fatalf("normalize context with unresolvable repo root = (%q, %t), want empty recognized path", got, ok)
	}
}

func TestLooksLikeWindowsAbsoluteContextPathRecognizesMixedUNCSeparators(t *testing.T) {
	for _, value := range []string{
		`\\server\share\project\main.js`,
		`//server/share/project/main.js`,
		`\/server/share/project/main.js`,
		`/\server/share/project/main.js`,
	} {
		if !looksLikeWindowsAbsoluteContextPath(value) {
			t.Fatalf("expected %q to be treated as UNC-like absolute context", value)
		}
	}
	if looksLikeWindowsAbsoluteContextPath("/") {
		t.Fatal("single slash must not be treated as a UNC-like absolute context")
	}
}

func fileURLForRuntimeTest(pathValue, host string) string {
	pathValue = filepath.ToSlash(pathValue)
	if !strings.HasPrefix(pathValue, "/") {
		pathValue = "/" + pathValue
	}
	return (&url.URL{Scheme: "file", Host: host, Path: pathValue}).String()
}
