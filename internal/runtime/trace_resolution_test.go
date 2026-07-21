package runtime

import "testing"

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

func TestPythonRuntimeSymbolStripsFileSuffixes(t *testing.T) {
	cases := []struct {
		module string
		want   string
	}{
		{module: "requests/sessions.py", want: "sessions"},
		{module: "requests/sessions.pyc", want: "sessions"},
		{module: "requests/sessions.pyo", want: "sessions"},
		{module: "requests.sessions", want: "sessions"},
	}
	for _, tc := range cases {
		if got := runtimeSymbolFromModuleForLanguage(tc.module, runtimeLanguagePython, "requests"); got != tc.want {
			t.Fatalf("expected Python symbol %q for %q, got %q", tc.want, tc.module, got)
		}
	}
}

func TestPythonRuntimeResolutionBranches(t *testing.T) {
	if got := normalizeRuntimeDependency("  My__Package  ", runtimeLanguageJSTS); got != "My__Package" {
		t.Fatalf("expected non-Python dependency to preserve trimmed value, got %q", got)
	}
	if got := normalizeRuntimeDependency("  ", runtimeLanguagePython); got != "" {
		t.Fatalf("expected blank dependency to stay blank, got %q", got)
	}
	if got := normalizeRuntimeDependency(" My__Package ", runtimeLanguagePython); got != "my-package" {
		t.Fatalf("expected Python dependency to use canonical PyPI key, got %q", got)
	}
	if got := dependencyFromEventForLanguage(Event{Dependency: "PIL"}, runtimeLanguagePython); got != "pillow" {
		t.Fatalf("expected direct Python dependency alias to normalize, got %q", got)
	}
	if got := dependencyFromEventForLanguage(Event{Resolved: "file:///repo/.venv/lib/python3.12/site-packages/httpx/_client.py"}, runtimeLanguagePython); got != "httpx" {
		t.Fatalf("expected Python dependency from resolved path, got %q", got)
	}
	if got := dependencyFromPythonResolvedPath("/repo/app.py"); got != "" {
		t.Fatalf("expected non-site-packages path to be ignored, got %q", got)
	}

	resolvedRequests := "file:///repo/.venv/lib/python3.12/site-packages/requests/sessions.py"
	if got := pythonRuntimeModuleFromEvent(Event{Module: "other.module", Resolved: resolvedRequests}, "requests"); got != "requests" {
		t.Fatalf("expected resolved Python module fallback, got %q", got)
	}
	if got := pythonRuntimeModuleFromEvent(Event{Module: "bs4", Dependency: "beautifulsoup4"}, "beautifulsoup4"); got != "bs4" {
		t.Fatalf("expected direct dependency override to keep Python module, got %q", got)
	}
	if got := pythonRuntimeModuleFromEvent(Event{Module: "other.module"}, "requests"); got != "requests" {
		t.Fatalf("expected Python module fallback to dependency, got %q", got)
	}
}

func TestPythonRuntimeResolvedPathBranches(t *testing.T) {
	cases := []struct {
		name       string
		resolved   string
		dependency string
		want       string
	}{
		{name: "blank", resolved: "", want: ""},
		{name: "empty site-packages suffix", resolved: "file:///repo/.venv/lib/python3.12/site-packages/", want: ""},
		{name: "dist info", resolved: "file:///repo/.venv/lib/python3.12/site-packages/requests-2.32.0.dist-info/METADATA", want: ""},
		{name: "egg info", resolved: "file:///repo/.venv/lib/python3.12/site-packages/demo.egg-info/PKG-INFO", want: ""},
		{name: "dependency mismatch", resolved: "file:///repo/.venv/lib/python3.12/site-packages/requests/sessions.py", dependency: "httpx", want: ""},
		{name: "dependency match", resolved: "file:///repo/.venv/lib/python3.12/site-packages/requests/sessions.py", dependency: "requests", want: "requests"},
		{name: "dist packages", resolved: "/usr/lib/python3/dist-packages/httpx/_client.py", dependency: "httpx", want: "httpx"},
	}
	for _, tc := range cases {
		if got := pythonModuleFromResolvedPath(tc.resolved, tc.dependency); got != tc.want {
			t.Fatalf("%s: expected resolved module %q, got %q", tc.name, tc.want, got)
		}
	}
}

func TestPythonRuntimeModulePartGuards(t *testing.T) {
	for _, module := range []string{"", ".", "/abs/module.py", "node:fs"} {
		if got := dependencyFromPythonModule(module); got != "" {
			t.Fatalf("expected invalid Python module %q to be ignored, got %q", module, got)
		}
	}
	for _, module := range []string{"", "requests", "requests.__init__"} {
		if got := pythonRuntimeSymbolFromModule(module); got != "" {
			t.Fatalf("expected no Python runtime symbol for %q, got %q", module, got)
		}
	}
}

func TestDependencyFromResolvedPathBlankValue(t *testing.T) {
	if got := dependencyFromResolvedPath("   "); got != "" {
		t.Fatalf("expected blank resolved path to produce no dependency, got %q", got)
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
