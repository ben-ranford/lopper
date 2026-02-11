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
	if trace.DependencyLoads["@scope/pkg"] != 1 {
		t.Fatalf("expected @scope/pkg load count=1, got %d", trace.DependencyLoads["@scope/pkg"])
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
	if dep := dependencyFromSpecifier(" "); dep != "" {
		t.Fatalf("expected empty dependency for blank specifier, got %q", dep)
	}
	if dep := dependencyFromSpecifier("./local"); dep != "" {
		t.Fatalf("expected empty dependency for local specifier, got %q", dep)
	}
	if dep := dependencyFromSpecifier("@scope/pkg/path"); dep != "@scope/pkg" {
		t.Fatalf("expected scoped dependency, got %q", dep)
	}
	if dep := dependencyFromResolvedPath("file:///repo/node_modules/@scope/pkg/lib/index.js"); dep != "@scope/pkg" {
		t.Fatalf("expected scoped dependency from resolved path, got %q", dep)
	}
	if dep := dependencyFromResolvedPath("/repo/node_modules/lodash/map.js"); dep != "lodash" {
		t.Fatalf("expected lodash dependency from resolved path, got %q", dep)
	}
	if dep := dependencyFromResolvedPath("/repo/no-node-modules/here.js"); dep != "" {
		t.Fatalf("expected empty dependency for non-node_modules path, got %q", dep)
	}
	if dep := dependencyFromEvent(Event{Resolved: "/repo/node_modules/react/index.js"}); dep != "react" {
		t.Fatalf("expected dependency from resolved event, got %q", dep)
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
	if dep := dependencyFromSpecifier("@scope"); dep != "" {
		t.Fatalf("expected empty scoped dependency without package segment, got %q", dep)
	}
	if dep := dependencyFromSpecifier("/abs/path"); dep != "" {
		t.Fatalf("expected empty dependency for absolute path, got %q", dep)
	}
	if dep := dependencyFromSpecifier("node:fs"); dep != "" {
		t.Fatalf("expected empty dependency for node protocol, got %q", dep)
	}

	if dep := dependencyFromResolvedPath("file:///repo/node_modules/"); dep != "" {
		t.Fatalf("expected empty dependency for empty node_modules suffix, got %q", dep)
	}
	if dep := dependencyFromResolvedPath("file:///repo/node_modules/@scope"); dep != "" {
		t.Fatalf("expected empty dependency for malformed scoped path, got %q", dep)
	}
	if dep := dependencyFromResolvedPath("file:///repo/node_modules/pkg/sub/index.js"); dep != "pkg" {
		t.Fatalf("expected pkg dependency, got %q", dep)
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
