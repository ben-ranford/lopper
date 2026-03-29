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
