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

func TestLooksLikePackageStyleRuntimeContextLabel(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "lodash/fp.js", want: true},
		{value: "@scope/pkg/index.js", want: true},
		{value: "src/main.js", want: true},
		{value: "C:/Users/alice/private.js", want: false},
		{value: `C:\Users\alice\private.js`, want: false},
		{value: "C:Users/alice/private.js", want: false},
		{value: "//server/share/alice/private.js", want: false},
		{value: `\\server\share\alice\private.js`, want: false},
		{value: "//./C:/Users/alice/private.js", want: false},
		{value: `\\.\C:\Users\alice\private.js`, want: false},
		{value: "//?/C:/Users/alice/private.js", want: false},
		{value: `\\?\C:\Users\alice\private.js`, want: false},
		{value: "//?/UNC/server/share/alice/private.js", want: false},
		{value: `\\?\UNC\server\share\alice\private.js`, want: false},
		{value: "/??/C:/Users/alice/private.js", want: false},
		{value: `\??\C:\Users\alice\private.js`, want: false},
		{value: "/Device/HarddiskVolume1/Users/alice/private.js", want: false},
		{value: `\Device\HarddiskVolume1\Users\alice\private.js`, want: false},
		{value: "https:/example.com/private.js", want: false},
		{value: "../src/main.js", want: false},
		{value: "src/../../Users/alice/private.js", want: false},
		{value: "/tmp/main.js", want: false},
		{value: "~/private/main.js", want: false},
		{value: "pkg/.env/private.js", want: false},
		{value: "pkg/~/.ssh/id_rsa", want: false},
		{value: "@scope", want: false},
		{value: "pkg//index.js", want: false},
		{value: "node:fs", want: true},
		{value: "node:fs/promises", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			if got := looksLikePackageStyleRuntimeContextLabel(tc.value); got != tc.want {
				t.Fatalf("looksLikePackageStyleRuntimeContextLabel(%q) = %t, want %t", tc.value, got, tc.want)
			}
		})
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
	if got := resolveRuntimeContextPath("   ", nil); got != "" {
		t.Fatalf("expected blank path to normalize empty, got %q", got)
	}
}

func TestResolveRuntimeContextPathUsesDefaultEvalSymlinksWhenResolverIsNil(t *testing.T) {
	repo := t.TempDir()
	modulePath := filepath.Join(repo, "src", "main.js")
	if err := os.MkdirAll(filepath.Dir(modulePath), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(modulePath, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write repo module: %v", err)
	}

	want, err := filepath.EvalSymlinks(modulePath)
	if err != nil {
		t.Fatalf("eval symlinks module path: %v", err)
	}
	want = filepath.Clean(want)
	if got := resolveRuntimeContextPath(modulePath, nil); got != want {
		t.Fatalf("expected default symlink resolution to produce %q, got %q", want, got)
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

func TestRuntimeContextLexicallyWithinRepoReturnsFalseForBlankInputs(t *testing.T) {
	tests := []struct {
		name     string
		repoRoot string
		value    string
	}{
		{name: "blank repo root", repoRoot: " ", value: "/repo/src/main.js"},
		{name: "blank value", repoRoot: "/repo", value: " "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if runtimeContextLexicallyWithinRepo(tc.repoRoot, tc.value) {
				t.Fatalf("expected lexical repo check to reject repoRoot=%q value=%q", tc.repoRoot, tc.value)
			}
		})
	}
}

func TestRuntimeContextRepoRelativeReturnsFalseWhenRepoRootIsBlank(t *testing.T) {
	if rel, ok := runtimeContextRepoRelative("", "/repo/src/main.js"); ok || rel != "" {
		t.Fatalf("expected blank repo root to fail relative conversion, got (%q, %t)", rel, ok)
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
		{name: "reject forward slash windows drive path", value: "C:/Users/alice/private.js", want: ""},
		{name: "reject backslash windows drive path", value: `C:\Users\alice\private.js`, want: ""},
		{name: "reject forward slash UNC path", value: "//server/share/alice/private.js", want: ""},
		{name: "reject backslash UNC path", value: `\\server\share\alice\private.js`, want: ""},
		{name: "reject forward slash device path", value: "//./C:/Users/alice/private.js", want: ""},
		{name: "reject backslash device path", value: `\\.\C:\Users\alice\private.js`, want: ""},
		{name: "reject forward slash verbatim drive path", value: "//?/C:/Users/alice/private.js", want: ""},
		{name: "reject backslash verbatim drive path", value: `\\?\C:\Users\alice\private.js`, want: ""},
		{name: "reject forward slash verbatim UNC path", value: "//?/UNC/server/share/alice/private.js", want: ""},
		{name: "reject backslash verbatim UNC path", value: `\\?\UNC\server\share\alice\private.js`, want: ""},
		{name: "reject forward slash object manager alias", value: "/??/C:/Users/alice/private.js", want: ""},
		{name: "reject backslash object manager alias", value: `\??\C:\Users\alice\private.js`, want: ""},
		{name: "reject forward slash object manager path", value: "/Device/HarddiskVolume1/Users/alice/private.js", want: ""},
		{name: "reject backslash object manager path", value: `\Device\HarddiskVolume1\Users\alice\private.js`, want: ""},
		{name: "rejected data scheme", value: "data:text/plain,secret", want: ""},
		{name: "rejected mailto scheme", value: "mailto:test@example.com", want: ""},
		{name: "rejected https opaque", value: "https:foo", want: ""},
		{name: "rejected https single slash", value: "https:/foo", want: ""},
		{name: "relative traversal", value: "src/../../Users/alice/private.js", want: ""},
		{name: "home relative path", value: "~/private/main.js", want: ""},
		{name: "encoded traversal", value: strings.TrimSuffix(repoURL, "/") + "/src%2F..%2F..%2FUsers/alice/main.js", want: ""},
		{name: "malformed escape", value: strings.TrimSuffix(repoURL, "/") + "/src/bad%ZZ.js", want: ""},
		{name: "node package label", value: "node:internal/modules/cjs/loader", want: "node:internal/modules/cjs/loader"},
		{name: "package subpath label", value: "lodash/map", want: "lodash/map"},
		{name: "package file label", value: "lodash/fp.js", want: "lodash/fp.js"},
		{name: "scoped package file label", value: "@scope/pkg/index.js", want: "@scope/pkg/index.js"},
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
	got, ok := normalizeRuntimeContextPath("src/main.js", traceLoadOptions{
		repoRoot:        "/repo",
		resolveRepoRoot: func(string) string { return "" },
	})
	if !ok || got != "" {
		t.Fatalf("normalize context with unresolvable repo root = (%q, %t), want empty recognized path", got, ok)
	}
}

func TestNormalizeRuntimeContextPathRejectsHostilePathsBeforeFilesystemProbe(t *testing.T) {
	repo := t.TempDir()
	var probed []string
	evalSymlinks := func(path string) (string, error) {
		probed = append(probed, filepath.Clean(path))
		return "", os.ErrNotExist
	}

	tests := []string{
		"/etc/passwd",
		"../outside.js",
		"./../../outside.js",
		`\\server\share\secret.js`,
		`//server/share/secret.js`,
		`/\server/share/secret.js`,
		`file:///etc/passwd`,
		"file://localhost/etc/passwd",
		strings.TrimSuffix(fileURLForRuntimeTest(repo, ""), "/") + "/src%2F..%2F..%2Foutside.js",
	}
	opts := traceLoadOptions{
		repoRoot:         repo,
		resolvedRepoRoot: resolvedRuntimeRepoRoot(repo),
		evalSymlinks:     evalSymlinks,
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			probed = nil
			got, ok := normalizeRuntimeContextPath(value, opts)
			if !ok || got != "" {
				t.Fatalf("normalize hostile context %q = (%q, %t), want empty recognized path", value, got, ok)
			}
			if len(probed) != 0 {
				t.Fatalf("expected hostile context %q to avoid filesystem probes, got %#v", value, probed)
			}
		})
	}
}

func TestNormalizeRuntimeContextPathProbesTrustedRepoLocalCandidatesOnly(t *testing.T) {
	repo := t.TempDir()
	modulePath := filepath.Join(repo, "src", "main.js")
	if err := os.MkdirAll(filepath.Dir(modulePath), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(modulePath, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write repo module: %v", err)
	}

	var probed []string
	evalSymlinks := func(path string) (string, error) {
		probed = append(probed, filepath.Clean(path))
		return filepath.EvalSymlinks(path)
	}

	opts := traceLoadOptions{
		repoRoot:         repo,
		resolvedRepoRoot: resolvedRuntimeRepoRoot(repo),
		evalSymlinks:     evalSymlinks,
	}
	got, ok := normalizeRuntimeContextPath(fileURLForRuntimeTest(modulePath, ""), opts)
	if !ok || got != "src/main.js" {
		t.Fatalf("normalize trusted repo-local file URL = (%q, %t), want (%q, true)", got, ok, "src/main.js")
	}
	if len(probed) != 1 || probed[0] != filepath.Clean(modulePath) {
		t.Fatalf("expected one probe for trusted candidate %q, got %#v", modulePath, probed)
	}
}

func TestNormalizeRuntimeContextPathPreservesSymlinkAliasAttribution(t *testing.T) {
	repoRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "repo-alias")
	modulePath := filepath.Join(repoRoot, "src", "main.js")
	if err := os.MkdirAll(filepath.Dir(modulePath), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(modulePath, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write repo module: %v", err)
	}
	if err := os.Symlink(repoRoot, aliasRoot); err != nil {
		t.Fatalf("symlink repo alias: %v", err)
	}

	got, ok := normalizeRuntimeContextPath(filepath.Join(aliasRoot, "src", "main.js"), traceLoadOptions{
		repoRoot:         aliasRoot,
		resolvedRepoRoot: resolvedRuntimeRepoRoot(aliasRoot),
	})
	if !ok || got != "src/main.js" {
		t.Fatalf("normalize symlinked repo alias = (%q, %t), want (%q, true)", got, ok, "src/main.js")
	}
}

func TestNormalizeRuntimeContextPathRedactsRemovedSymlinkAlias(t *testing.T) {
	repoRoot := t.TempDir()
	aliasParent := t.TempDir()
	aliasRoot := filepath.Join(aliasParent, "repo-alias")
	modulePath := filepath.Join(repoRoot, "src", "main.js")
	if err := os.MkdirAll(filepath.Dir(modulePath), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(modulePath, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write repo module: %v", err)
	}
	if err := os.Symlink(repoRoot, aliasRoot); err != nil {
		t.Fatalf("symlink repo alias: %v", err)
	}

	opts := traceLoadOptions{
		repoRoot:         aliasRoot,
		resolvedRepoRoot: resolvedRuntimeRepoRoot(aliasRoot),
	}
	candidate := filepath.Join(aliasRoot, "src", "main.js")
	if err := os.Remove(aliasRoot); err != nil {
		t.Fatalf("remove repo alias: %v", err)
	}

	got, ok := normalizeRuntimeContextPath(candidate, opts)
	if !ok || got != "" {
		t.Fatalf("normalize removed repo alias = (%q, %t), want empty recognized path", got, ok)
	}
	for _, forbidden := range []string{repoRoot, aliasRoot, candidate} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("removed alias normalization leaked host path %q in %q", forbidden, got)
		}
	}
}

func TestNormalizeRuntimeContextPathRedactsRetargetedSymlinkAlias(t *testing.T) {
	repoRoot := t.TempDir()
	aliasParent := t.TempDir()
	aliasRoot := filepath.Join(aliasParent, "repo-alias")
	retargetRoot := filepath.Join(t.TempDir(), "private-root")
	modulePath := filepath.Join(repoRoot, "src", "main.js")
	privatePath := filepath.Join(retargetRoot, "src", "private.js")
	if err := os.MkdirAll(filepath.Dir(modulePath), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(privatePath), 0o755); err != nil {
		t.Fatalf("mkdir private src: %v", err)
	}
	if err := os.WriteFile(modulePath, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write repo module: %v", err)
	}
	if err := os.WriteFile(privatePath, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write private module: %v", err)
	}
	if err := os.Symlink(repoRoot, aliasRoot); err != nil {
		t.Fatalf("symlink repo alias: %v", err)
	}

	opts := traceLoadOptions{
		repoRoot:         aliasRoot,
		resolvedRepoRoot: resolvedRuntimeRepoRoot(aliasRoot),
	}
	candidate := filepath.Join(aliasRoot, "src", "private.js")
	if err := os.Remove(aliasRoot); err != nil {
		t.Fatalf("remove repo alias before retarget: %v", err)
	}
	if err := os.Symlink(retargetRoot, aliasRoot); err != nil {
		t.Fatalf("retarget repo alias: %v", err)
	}

	got, ok := normalizeRuntimeContextPath(candidate, opts)
	if !ok || got != "" {
		t.Fatalf("normalize retargeted repo alias = (%q, %t), want empty recognized path", got, ok)
	}
	for _, forbidden := range []string{repoRoot, aliasRoot, retargetRoot, candidate, privatePath} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("retargeted alias normalization leaked host path %q in %q", forbidden, got)
		}
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
