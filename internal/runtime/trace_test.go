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

const (
	scopePkgDependency         = "@scope/pkg"
	lodashMapModule            = "lodash/map"
	expectedGotFormat          = "%s: expected %q, got %q"
	leftPadDependency          = "left-pad"
	leftPadModule              = "left-pad/index"
	leftPadResolvedIndexModule = "/repo/node_modules/left-pad/index.js"
)

func loadTraceFromContent(t *testing.T, content string) (Trace, error) {
	t.Helper()
	return Load(testutil.WriteTempFile(t, "runtime.ndjson", content))
}

func TestLoadTrace(t *testing.T) {
	trace, err := loadTraceFromContent(
		t,
		`{"kind":"resolve","module":"`+lodashMapModule+`","resolved":"file:///repo/node_modules/lodash/map.js"}`+"\n"+
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
	if got := trace.DependencyModules["lodash"][lodashMapModule]; got != 1 {
		t.Fatalf("expected lodash module count 1, got %d", got)
	}
	if got := trace.DependencySymbols["lodash"][lodashMapModule+"\x00map"]; got != 1 {
		t.Fatalf("expected lodash symbol count 1, got %d", got)
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
	}, AnnotateOptions{})

	if annotated.Dependencies[0].RuntimeUsage == nil || !annotated.Dependencies[0].RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected alpha to be runtime-only annotated")
	}
	if annotated.Dependencies[0].RuntimeUsage.Correlation != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected alpha runtime-only correlation, got %#v", annotated.Dependencies[0].RuntimeUsage)
	}
	if len(annotated.Dependencies[0].RuntimeUsage.Modules) != 0 {
		t.Fatalf("did not expect modules for alpha runtime usage")
	}
	if annotated.Dependencies[1].RuntimeUsage == nil || annotated.Dependencies[1].RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected beta to be runtime annotated but not runtime-only")
	}
	if annotated.Dependencies[1].RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap {
		t.Fatalf("expected beta overlap correlation, got %#v", annotated.Dependencies[1].RuntimeUsage)
	}
}

func TestAnnotateStaticOnly(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name: "alpha",
				UsedImports: []report.ImportUse{
					{Name: "map", Module: "alpha"},
				},
			},
		},
	}

	annotated := Annotate(rep, Trace{DependencyLoads: map[string]int{}}, AnnotateOptions{})
	if annotated.Dependencies[0].RuntimeUsage == nil {
		t.Fatalf("expected static-only runtime usage annotation")
	}
	if annotated.Dependencies[0].RuntimeUsage.Correlation != report.RuntimeCorrelationStaticOnly {
		t.Fatalf("expected static-only correlation, got %#v", annotated.Dependencies[0].RuntimeUsage)
	}
	if annotated.Dependencies[0].RuntimeUsage.LoadCount != 0 {
		t.Fatalf("expected zero load count for static-only annotation, got %d", annotated.Dependencies[0].RuntimeUsage.LoadCount)
	}
	if annotated.Dependencies[0].RuntimeUsage.RuntimeOnly {
		t.Fatalf("did not expect runtime-only=true for static-only annotation")
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
			t.Fatalf(expectedGotFormat, tc.name, tc.want, tc.got)
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
	annotated := Annotate(rep, Trace{DependencyLoads: map[string]int{"java-dep": 3}}, AnnotateOptions{})
	if annotated.Dependencies[0].RuntimeUsage != nil {
		t.Fatalf("did not expect runtime usage for unsupported language")
	}
	if annotated.Dependencies[1].RuntimeUsage == nil {
		return
	}
	t.Fatalf("did not expect runtime usage when js dependency has no static imports and no runtime loads")
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
			t.Fatalf(expectedGotFormat, tc.name, tc.want, tc.got)
		}
	}
}

func TestDependencyFromEventPrefersModule(t *testing.T) {
	event := Event{
		Module:   leftPadModule,
		Resolved: "/repo/node_modules/right-pad/index.js",
	}
	if dep := dependencyFromEvent(event); dep != leftPadDependency {
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
	trace, err := loadTraceFromContent(t, "\n   \n{\"module\":\""+lodashMapModule+"\"}\n")
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

func TestAnnotateAddsRuntimeOnlyDependencyRows(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "lodash", Language: "js-ts"},
		},
	}
	trace := Trace{
		DependencyLoads: map[string]int{
			"lodash": 1,
			"chalk":  2,
		},
		DependencyModules: map[string]map[string]int{
			"chalk": {"chalk/index.js": 2},
		},
		DependencySymbols: map[string]map[string]int{
			"chalk": {"index": 2},
		},
	}

	annotated := Annotate(rep, trace, AnnotateOptions{IncludeRuntimeOnlyRows: true})
	if len(annotated.Dependencies) != 2 {
		t.Fatalf("expected runtime-only row to be added, got %d dependencies", len(annotated.Dependencies))
	}

	var chalk *report.DependencyReport
	for i := range annotated.Dependencies {
		if annotated.Dependencies[i].Name == "chalk" {
			chalk = &annotated.Dependencies[i]
			break
		}
	}
	if chalk == nil || chalk.RuntimeUsage == nil {
		t.Fatalf("expected runtime-only chalk dependency row")
	}
	if chalk.RuntimeUsage.Correlation != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected runtime-only correlation, got %#v", chalk.RuntimeUsage)
	}
	if len(chalk.RuntimeUsage.Modules) == 0 || chalk.RuntimeUsage.Modules[0].Module != "chalk/index.js" {
		t.Fatalf("expected runtime modules on runtime-only row, got %#v", chalk.RuntimeUsage.Modules)
	}
}

func TestRuntimeModuleAndSymbolExtraction(t *testing.T) {
	module := runtimeModuleFromEvent(
		Event{Resolved: "/repo/node_modules/lodash/fp/map.js"},
		"lodash",
	)
	if module != "lodash/fp/map.js" {
		t.Fatalf("unexpected runtime module: %q", module)
	}
	symbol := runtimeSymbolFromModule(module, "lodash")
	if symbol != "map" {
		t.Fatalf("expected map symbol, got %q", symbol)
	}
}

func TestRuntimeModuleFromResolvedPathBranches(t *testing.T) {
	cases := []struct {
		name       string
		resolved   string
		dependency string
		want       string
	}{
		{name: "empty", resolved: "", dependency: "lodash", want: ""},
		{name: "no marker", resolved: "/repo/src/index.js", dependency: "lodash", want: ""},
		{name: "scoped missing package", resolved: "/repo/node_modules/@scope", dependency: "@scope/pkg", want: ""},
		{name: "dependency mismatch", resolved: leftPadResolvedIndexModule, dependency: "lodash", want: ""},
		{name: "scoped root", resolved: "/repo/node_modules/@scope/pkg/index.js", dependency: "@scope/pkg", want: "@scope/pkg/index.js"},
		{name: "scoped mismatch", resolved: "/repo/node_modules/@scope/pkg/index.js", dependency: "@scope/other", want: ""},
		{name: "simple root", resolved: "/repo/node_modules/lodash/index.js", dependency: "lodash", want: "lodash/index.js"},
	}
	for _, tc := range cases {
		if got := runtimeModuleFromResolvedPath(tc.resolved, tc.dependency); got != tc.want {
			t.Fatalf(expectedGotFormat, tc.name, tc.want, got)
		}
	}
}

func TestRuntimeSymbolFromModuleBranches(t *testing.T) {
	cases := []struct {
		name       string
		module     string
		dependency string
		want       string
	}{
		{name: "empty module", module: "", dependency: "lodash", want: ""},
		{name: "dependency root", module: "lodash", dependency: "lodash", want: ""},
		{name: "index fallback to dir", module: "lodash/fp/index.js", dependency: "lodash", want: "fp"},
		{name: "normal symbol", module: "lodash/map.js", dependency: "lodash", want: "map"},
	}
	for _, tc := range cases {
		if got := runtimeSymbolFromModule(tc.module, tc.dependency); got != tc.want {
			t.Fatalf(expectedGotFormat, tc.name, tc.want, got)
		}
	}
}

func TestRuntimeModulesAndSymbolsFormatting(t *testing.T) {
	if got := runtimeModules(nil); got != nil {
		t.Fatalf("expected nil runtime modules for empty input, got %#v", got)
	}
	modules := runtimeModules(map[string]int{
		"lodash/filter": 1,
		lodashMapModule: 2,
	})
	if len(modules) != 2 || modules[0].Module != lodashMapModule {
		t.Fatalf("expected module sorting by count, got %#v", modules)
	}

	if got := runtimeSymbols(nil); got != nil {
		t.Fatalf("expected nil runtime symbols for empty input, got %#v", got)
	}
	symbols := runtimeSymbols(map[string]int{
		lodashMapModule + "\x00map": 3,
		"lodash/filter\x00filter":   1,
		"broken":                    2,
		"lodash/fp\x00fp":           2,
		"lodash/reduce\x00reduce":   1,
		"lodash/chunk\x00chunk":     1,
	})
	if len(symbols) != 5 {
		t.Fatalf("expected top 5 runtime symbols, got %#v", symbols)
	}
	if symbols[0].Symbol != "map" || symbols[0].Module != lodashMapModule {
		t.Fatalf("expected map symbol first, got %#v", symbols[0])
	}
}

func TestAddCountGuards(t *testing.T) {
	target := map[string]map[string]int{}
	addCount(target, "", "x")
	addCount(target, "dep", "")
	if len(target) != 0 {
		t.Fatalf("expected guarded addCount to skip invalid entries, got %#v", target)
	}

	addSymbolCount(target, "dep", "dep/module", "")
	if len(target) != 0 {
		t.Fatalf("expected guarded addSymbolCount to skip empty symbols, got %#v", target)
	}
}

func TestRuntimeModuleFromEventFallbackBranches(t *testing.T) {
	if got := runtimeModuleFromEvent(Event{Module: leftPadModule}, leftPadDependency); got != leftPadModule {
		t.Fatalf("expected module specifier match, got %q", got)
	}
	if got := runtimeModuleFromEvent(Event{Module: "right-pad/index", Resolved: leftPadResolvedIndexModule}, leftPadDependency); got != leftPadDependency+"/index.js" {
		t.Fatalf("expected resolved-path fallback, got %q", got)
	}
	if got := runtimeModuleFromEvent(Event{}, leftPadDependency); got != leftPadDependency {
		t.Fatalf("expected dependency fallback when no module/resolved, got %q", got)
	}
}
