package runtime

import "testing"

func TestNormalizeRuntimeContextLabelReturnsEmptyForURLs(t *testing.T) {
	if got := normalizeRuntimeContextLabel("https://example.com/app.js"); got != "" {
		t.Fatalf("expected URL label to be redacted, got %q", got)
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
