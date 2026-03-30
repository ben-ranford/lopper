package runtime

import "testing"

func TestRuntimeModuleAndSymbolExtraction(t *testing.T) {
	module := runtimeModuleFromEvent(Event{Resolved: "/repo/node_modules/lodash/fp/map.js"}, "lodash")
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
		{name: "scoped missing package", resolved: "/repo/node_modules/@scope", dependency: scopePkgDependency, want: ""},
		{name: "dependency mismatch", resolved: leftPadResolvedIndexModule, dependency: "lodash", want: ""},
		{name: "scoped root", resolved: "/repo/node_modules/@scope/pkg/index.js", dependency: scopePkgDependency, want: scopePkgDependency + "/index.js"},
		{name: "scoped mismatch", resolved: "/repo/node_modules/@scope/pkg/index.js", dependency: "@scope/other", want: ""},
		{name: "simple root", resolved: "/repo/node_modules/lodash/index.js", dependency: "lodash", want: "lodash/index.js"},
	}
	for _, tc := range cases {
		if got := runtimeModuleFromResolvedPath(tc.resolved, tc.dependency); got != tc.want {
			t.Fatalf(expectedGotFormat, tc.name, tc.want, got)
		}
	}
}

func TestRuntimeModuleFromResolvedPathPackageRoots(t *testing.T) {
	if got := runtimeModuleFromResolvedPath("/repo/node_modules/lodash", "lodash"); got != "lodash" {
		t.Fatalf("expected package root module for unscoped dependency, got %q", got)
	}
	if got := runtimeModuleFromResolvedPath("/repo/node_modules/@scope/pkg", scopePkgDependency); got != scopePkgDependency {
		t.Fatalf("expected package root module for scoped dependency, got %q", got)
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

func TestRuntimeSymbolFromModuleRejectsEmptyFileNames(t *testing.T) {
	if got := runtimeSymbolFromModule("lodash/.js", "lodash"); got != "" {
		t.Fatalf("expected empty symbol for empty filename stem, got %q", got)
	}
}

func TestRuntimeModulesAndSymbolsFormatting(t *testing.T) {
	if got := runtimeModules(nil); len(got) != 0 {
		t.Fatalf("expected nil runtime modules for empty input, got %#v", got)
	}
	modules := runtimeModules(map[string]int{
		"lodash/filter": 1,
		lodashMapModule: 2,
	})
	if len(modules) != 2 || modules[0].Module != lodashMapModule {
		t.Fatalf("expected module sorting by count, got %#v", modules)
	}

	if got := runtimeSymbols(nil); len(got) != 0 {
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

func TestRuntimeModulesSortsTieByModuleName(t *testing.T) {
	modules := runtimeModules(map[string]int{
		zetaIndexModule:  1,
		alphaIndexModule: 1,
	})
	if len(modules) != 2 || modules[0].Module != alphaIndexModule || modules[1].Module != zetaIndexModule {
		t.Fatalf("expected alphabetical order for equal counts, got %#v", modules)
	}
}

func TestRuntimeSymbolsSortsEqualSymbolsByModuleName(t *testing.T) {
	symbols := runtimeSymbols(map[string]int{
		zetaIndexModule + "\x00same":  1,
		alphaIndexModule + "\x00same": 1,
	})
	if len(symbols) != 2 || symbols[0].Module != alphaIndexModule || symbols[1].Module != zetaIndexModule {
		t.Fatalf("expected equal symbols to sort by module name, got %#v", symbols)
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
