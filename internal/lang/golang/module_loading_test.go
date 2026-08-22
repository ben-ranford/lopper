package golang

import (
	"strings"
	"testing"
)

func TestOversizedRootGoModBehaviorKeepsLongGodebugDirectivesBeforeModule(t *testing.T) {
	for name, lines := range map[string][]string{
		"line": {
			"godebug default=" + strings.Repeat("x", 70*1024),
			"module example.com/root",
		},
		"block": {
			"godebug (",
			"default=" + strings.Repeat("x", 70*1024),
			")",
			"module example.com/root",
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			writeOversizedRootGoModLines(t, repo, lines...)

			requireOversizedRootModulePath(t, repo, "module path extraction after long godebug directive")
		})
	}
}

func TestOversizedRootGoModBehaviorRejectsMalformedLongGodebugAndIgnoreDirectives(t *testing.T) {
	for name, lines := range map[string][]string{
		"godebug line": {
			"module example.com/root",
			"godebug default=" + strings.Repeat("x", 70*1024) + " extra",
		},
		"godebug block": {
			"module example.com/root",
			"godebug (",
			"default=" + strings.Repeat("x", 70*1024) + " extra",
			")",
		},
		"ignore line": {
			"module example.com/root",
			"ignore ./" + strings.Repeat("nested/", 10*1024) + "dir extra",
		},
		"ignore block": {
			"module example.com/root",
			"ignore (",
			"./" + strings.Repeat("nested/", 10*1024) + "dir extra",
			")",
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			writeOversizedRootGoModLines(t, repo, lines...)

			requireNoTrustedOversizedRootModuleMetadata(t, repo)
		})
	}
}

func TestOversizedRootGoModKeepsLongSupportedFallbackDirectives(t *testing.T) {
	longGodebug := "default=" + strings.Repeat("x", 70*1024)
	longIgnore := "./" + strings.Repeat("nested/", 10*1024) + "dir"
	longVersionedReplace := "example.com/" + strings.Repeat("x", 70*1024) + " v1.2.3"
	longToolchain := "go1." + strings.Repeat("0", 70*1024)
	longQuotedOldPath := `replace "example.com/` + strings.Repeat("x", 70*1024) + `" => "./a//b"`

	for name, tc := range map[string]struct {
		lines    []string
		wantPath string
	}{
		"godebug line": {
			lines: []string{
				"godebug " + longGodebug,
				"module example.com/root",
			},
			wantPath: "example.com/root",
		},
		"ignore line": {
			lines: []string{
				"ignore " + longIgnore,
				"module example.com/root",
			},
			wantPath: "example.com/root",
		},
		"godebug block": {
			lines: []string{
				"godebug (",
				longGodebug,
				")",
				"module example.com/root",
			},
			wantPath: "example.com/root",
		},
		"ignore block": {
			lines: []string{
				"ignore (",
				longIgnore,
				")",
				"module example.com/root",
			},
			wantPath: "example.com/root",
		},
		"replace unquoted versioned target": {
			lines: []string{
				"module example.com/root",
				"replace example.com/a => " + longVersionedReplace,
			},
			wantPath: "example.com/root",
		},
		"replace quoted old path with quoted comment markers in suffix": {
			lines: []string{
				"module example.com/root",
				longQuotedOldPath,
			},
			wantPath: "example.com/root",
		},
		"toolchain line": {
			lines: []string{
				"toolchain " + longToolchain,
				"module example.com/root",
			},
			wantPath: "example.com/root",
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			writeOversizedRootGoModLines(t, repo, tc.lines...)

			requireOversizedRootModulePathMatch(t, repo, tc.wantPath, "module path extraction with long "+name)
		})
	}
}
