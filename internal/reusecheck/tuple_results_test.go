package reusecheck

import "testing"

func TestTupleSourceResults(t *testing.T) {
	for _, tc := range []struct{ results, setup, receiver string }{
		{"s.DependencyStats, error", "measured, err := factory(); _ = err", "measured"},
		{"error, s.DependencyStats", "_, measured := factory()", "measured"},
		{"int, error, *s.DependencyStats", "_, _, measured := factory()", "(*measured)"},
		{"first, second s.DependencyStats", "_, measured := factory()", "(&measured)"},
		{"s.DependencyStats, error", "var measured, err = factory(); _ = err", "measured"},
		{"s.DependencyStats, error", "var measured, err = factory(); _ = err; alias := measured", "alias"},
	} {
		t.Run(tc.setup+tc.results, func(t *testing.T) {
			declaration := "func factory() (" + tc.results + ") { panic(0) }"
			checkCollectionProvenance(t, declaration, collectionProvenanceUse{"unused string", tc.setup, tc.receiver, false}, true)
		})
	}
	checkSourceFunctionReportMapping(t, "func factory() (s.DependencyStats, error) { panic(0) }; var measured, err = factory()", "", "measured", true)
}

func TestTupleCollectionAndCallableResults(t *testing.T) {
	for _, tc := range []struct{ results, setup string }{
		{"[]s.DependencyStats, error", "values, _ := factory(); measured := values[0]"},
		{"error, map[string]s.DependencyStats", `_, values := factory(); measured, ok := values["key"]; _ = ok`},
		{"chan s.DependencyStats, error", "values, _ := factory(); measured, ok := <-values; _ = ok"},
		{"func() s.DependencyStats, error", "build, _ := factory(); measured := build()"},
		{"error, func() []s.DependencyStats", "_, build := factory(); measured := build()[0]"},
	} {
		declaration := "func factory() (" + tc.results + ") { panic(0) }"
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"unused string", tc.setup, "measured", false}, true)
	}
	checkSourceFunctionReportMapping(t, "func factory() (func() s.DependencyStats, error) { panic(0) }; var produce, err = factory()", "measured := produce()", "measured", true)
}

func TestTupleSourceResultGuards(t *testing.T) {
	for _, tc := range []struct{ declaration, setup string }{
		{"func factory() (s.DependencyStats, error) { panic(0) }", "measured := factory()"},
		{"func factory() (s.DependencyStats, error) { panic(0) }", "_, measured := factory()"},
		{"func factory() (s.DependencyStats, error) { panic(0) }", "measured, _, _ := factory()"},
		{"func factory() s.DependencyStats { panic(0) }", "measured, _ := factory()"},
		{"func factory(value int) (s.DependencyStats, error) { panic(0) }", "measured, _ := factory()"},
		{"type Stats s.DependencyStats; func factory() (Stats, error) { panic(0) }", "measured, _ := factory()"},
		{"func factory[T any]() (T, error) { panic(0) }", "measured, _ := factory[s.DependencyStats]()"},
		{"var factory, err = factory()", "measured, _ := factory()"},
		{"var first, err = factory(); var factory, other = first()", "measured, _ := factory()"},
	} {
		t.Run(tc.setup+tc.declaration, func(t *testing.T) {
			checkSourceFunctionReportMapping(t, tc.declaration, tc.setup, "measured", false)
		})
	}
}
