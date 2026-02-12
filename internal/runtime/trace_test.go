package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const scopePkgDependency = "@scope/pkg"

func loadTraceFromContent(t *testing.T, content string) (Trace, error) {
	t.Helper()
	return Load(testutil.WriteTempFile(t, "runtime.ndjson", content))
}

func TestLoadTrace(t *testing.T) {
	trace, err := loadTraceFromContent(
		t,
		`{"kind":"resolve","module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js"}`+"\n"+
			`{"kind":"require","module":"@scope/pkg/lib","resolved":"/repo/node_modules/@scope/pkg/lib/index.js"}`+"\n",
	)
	if err != nil {
		t.Fatalf("load trace: %v", err)
	}
	if trace.DependencyLoads["lodash"] != 1 {
		t.Fatalf("expected lodash load count=1, got %d", trace.DependencyLoads["lodash"])
	}
	if trace.DependencyLoads[scopePkgDependency] != 1 {
		t.Fatalf("expected %s load count=1, got %d", scopePkgDependency, trace.DependencyLoads[scopePkgDependency])
	}
}

func TestAnnotateRuntimeOnly(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "alpha"},
			{
				Name: "beta",
				UsedImports: []report.ImportUse{
					{Name: "map", Module: "beta"},
				},
			},
		},
	}

	annotated := Annotate(rep, Trace{
		DependencyLoads: map[string]int{
			"alpha": 2,
			"beta":  1,
		},
	})

	if annotated.Dependencies[0].RuntimeUsage == nil || !annotated.Dependencies[0].RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected alpha to be runtime-only annotated")
	}
	if annotated.Dependencies[1].RuntimeUsage == nil || annotated.Dependencies[1].RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected beta to be runtime annotated but not runtime-only")
	}
}

func TestLoadTraceInvalidLine(t *testing.T) {
	if _, err := loadTraceFromContent(t, "{not-json}\n"); err == nil {
		t.Fatalf("expected parse error for invalid NDJSON")
	}
}

func TestDependencyResolutionHelpers(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "blank specifier", got: dependencyFromSpecifier(" "), want: ""},
		{name: "local specifier", got: dependencyFromSpecifier("./local"), want: ""},
		{name: "scoped specifier", got: dependencyFromSpecifier("@scope/pkg/path"), want: scopePkgDependency},
		{name: "scoped resolved path", got: dependencyFromResolvedPath("file:///repo/node_modules/@scope/pkg/lib/index.js"), want: scopePkgDependency},
		{name: "resolved lodash path", got: dependencyFromResolvedPath("/repo/node_modules/lodash/map.js"), want: "lodash"},
		{name: "non node_modules path", got: dependencyFromResolvedPath("/repo/no-node-modules/here.js"), want: ""},
		{name: "event fallback", got: dependencyFromEvent(Event{Resolved: "/repo/node_modules/react/index.js"}), want: "react"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s: expected %q, got %q", tc.name, tc.want, tc.got)
		}
	}
}

func TestLoadTraceScannerErrTooLong(t *testing.T) {
	tooLong := strings.Repeat("x", 80*1024)
	_, err := loadTraceFromContent(t, tooLong)
	if err == nil {
		t.Fatalf("expected scanner error for oversized line")
	}
}

func TestAnnotateSkipsUnsupportedLanguageAndZeroLoads(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "java-dep", Language: "jvm"},
			{Name: "js-dep", Language: "js-ts"},
		},
	}
	annotated := Annotate(rep, Trace{DependencyLoads: map[string]int{"java-dep": 3}})
	if annotated.Dependencies[0].RuntimeUsage != nil {
		t.Fatalf("did not expect runtime usage for unsupported language")
	}
	if annotated.Dependencies[1].RuntimeUsage != nil {
		t.Fatalf("did not expect runtime usage when load count is zero")
	}
}

func TestAnnotateNoTraceLoadsReturnsOriginal(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{{Name: "x", Language: "js-ts"}},
	}
	annotated := Annotate(rep, Trace{})
	if annotated.Dependencies[0].RuntimeUsage != nil {
		t.Fatalf("did not expect runtime usage annotation")
	}
}

func TestDependencyFromSpecifierAndResolvedPathEdgeCases(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "scoped without package", got: dependencyFromSpecifier("@scope"), want: ""},
		{name: "absolute specifier path", got: dependencyFromSpecifier("/abs/path"), want: ""},
		{name: "node protocol", got: dependencyFromSpecifier("node:fs"), want: ""},
		{name: "empty node_modules suffix", got: dependencyFromResolvedPath("file:///repo/node_modules/"), want: ""},
		{name: "malformed scoped resolved path", got: dependencyFromResolvedPath("file:///repo/node_modules/@scope"), want: ""},
		{name: "package with subpath", got: dependencyFromResolvedPath("file:///repo/node_modules/pkg/sub/index.js"), want: "pkg"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s: expected %q, got %q", tc.name, tc.want, tc.got)
		}
	}
}

func TestDependencyFromEventPrefersModule(t *testing.T) {
	event := Event{
		Module:   "left-pad/index",
		Resolved: "/repo/node_modules/right-pad/index.js",
	}
	if dep := dependencyFromEvent(event); dep != "left-pad" {
		t.Fatalf("expected module-derived dependency, got %q", dep)
	}
}

func TestLoadTraceParseErrorIncludesLineNumber(t *testing.T) {
	_, err := loadTraceFromContent(t, "{\"module\":\"ok\"}\n{not-json}\n")
	if err == nil {
		t.Fatalf("expected parse error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected line number in parse error, got %v", err)
	}
}

func TestLoadTraceSkipsBlankLines(t *testing.T) {
	trace, err := loadTraceFromContent(t, "\n   \n{\"module\":\"lodash/map\"}\n")
	if err != nil {
		t.Fatalf("load trace: %v", err)
	}
	if got := trace.DependencyLoads["lodash"]; got != 1 {
		t.Fatalf("expected lodash load count 1, got %d", got)
	}
}

func TestLoadTraceMissingFileError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.ndjson"))
	if err == nil {
		t.Fatalf("expected missing-file error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}
