package reusecheck

import "testing"

func TestGenericFunctionExplicitStatsResults(t *testing.T) {
	declaration := "func factory[T, U any](first T, second U) s.DependencyStats { panic(0) }"
	for _, call := range []string{"factory(1, \"x\")", "factory[int](1, \"x\")", "factory[int, string](1, \"x\")"} {
		t.Run(call, func(t *testing.T) {
			checkSourceFunctionReportMapping(t, declaration, "measured := "+call, "(&measured)", true)
		})
	}
	checkSourceFunctionReportMapping(t, declaration, "bound := factory[int, string]; measured := bound(1, \"x\")", "measured", true)
	checkSourceFunctionReportMapping(t, "func factory[T any](value T) *s.DependencyStats { panic(0) }", "measured := factory(1)", "(*measured)", true)
	checkSourceFunctionReportMapping(t, "func factory[T any](value T) **s.DependencyStats { panic(0) }", "measured := factory[int](1)", "(*measured)", true)
}

func TestGenericFunctionUnprovenStatsResults(t *testing.T) {
	identity := "func factory[T any](value T) T { return value }"
	for _, call := range []string{"factory(1)", "factory[int](1)", "factory[s.DependencyStats](s.DependencyStats{})"} {
		checkSourceFunctionReportMapping(t, identity, "measured := "+call, "measured", false)
	}
	named := "type Stats s.DependencyStats; func factory[T any](value T) Stats { panic(0) }"
	checkSourceFunctionReportMapping(t, named, "measured := factory(1)", "measured", false)
	checkSourceFunctionReportMapping(t, "func factory[T any](value T) *s.DependencyStats { panic(0) }", "measured := factory[int](1)", "(&measured)", false)
}

func TestGenericSourceCallShape(t *testing.T) {
	declaration := "func factory[T any](value T) s.DependencyStats { panic(0) }"
	for _, call := range []string{"factory()", "factory(1, 2)", "factory[int, string](1)"} {
		checkSourceStatsCall(t, declaration, "", "", call, "")
	}
	checkSourceStatsCall(t, statsFactoryDeclaration, "", "", "factory[int]()", "")
	checkSourceStatsCall(t, "", "", "", "unknown[int]()", "")
	checkSourceStatsCall(t, declaration, "", "", "(factory[int])(1)", "s.DependencyStats")
}
