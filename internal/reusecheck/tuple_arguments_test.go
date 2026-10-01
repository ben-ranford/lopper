package reusecheck

import (
	"strings"
	"testing"
)

const tupleArgumentFactory = "func factory(first, second int) s.DependencyStats { panic(0) }"
const tupleArgumentPair = "func pair() (int, int) { panic(0) }"

func TestSourceTupleArgumentExpansion(t *testing.T) {
	for _, producer := range []string{
		tupleArgumentPair,
		"func pair() (first, second int) { panic(0) }",
		"var pair = func() (int, int) { panic(0) }",
		"type Pair func() (int, int); var pair Pair",
	} {
		declaration := tupleArgumentFactory + "; " + producer
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"unused string", "measured := factory(pair())", "measured", false}, true)
	}
	checkSourceStatsCall(t, tupleArgumentFactory+"; "+tupleArgumentPair, "", "bound := pair", "factory((bound()))", "s.DependencyStats")
	checkSourceStatsCall(t, tupleArgumentFactory+"; func pair[T any](value T) (int, int) { panic(0) }", "", "", "factory(pair[int](1))", "s.DependencyStats")
	checkSourceStatsCall(t, "func factory[T any](first, second T) s.DependencyStats { panic(0) }; "+tupleArgumentPair, "", "", "factory[int](pair())", "s.DependencyStats")
	checkSourceStatsCall(t, tupleArgumentFactory+"; type Source interface { Pair() (int, int) }", "source Source", "", "factory(source.Pair())", "s.DependencyStats")
	checkSourceStatsCall(t, tupleArgumentFactory+"; func single() int { return 1 }", "", "", "factory(single(), single())", "s.DependencyStats")
}

func TestTupleArgumentsToVariadicCalls(t *testing.T) {
	for _, parameters := range []string{"values ...int", "first int, values ...int", "first, second int, values ...int"} {
		declaration := "func factory(" + parameters + ") s.DependencyStats { panic(0) }; " + tupleArgumentPair
		checkSourceStatsCall(t, declaration, "", "", "factory(pair())", "s.DependencyStats")
	}
	declaration := "func factory(values ...int) s.DependencyStats { panic(0) }; func values() []int { panic(0) }"
	checkSourceStatsCall(t, declaration, "", "", "factory(values()...)", "s.DependencyStats")
	declaration = "func factory(first int, values ...int) s.DependencyStats { panic(0) }; func values() []int { panic(0) }"
	checkSourceStatsCall(t, declaration, "", "", "factory(1, values()...)", "s.DependencyStats")
	for _, call := range []string{"factory(1, pair())", "factory(pair()...)"} {
		checkSourceStatsCall(t, "func factory(values ...int) s.DependencyStats { panic(0) }; "+tupleArgumentPair, "", "", call, "")
	}
}

func TestTupleArgumentShapeGuards(t *testing.T) {
	for _, call := range []string{"factory(1, pair())", "factory(pair(), 1)", "factory(pair()...)", "factory(unknown())", "factory(s.Unknown())"} {
		checkSourceStatsCall(t, tupleArgumentFactory+"; "+tupleArgumentPair, "", "", call, "")
	}
	for _, producer := range []string{
		"func pair() int { return 1 }",
		"func pair() (int, int, int) { panic(0) }",
		"func pair() {}",
		"func pair(value int) (int, int) { panic(0) }",
	} {
		checkSourceStatsCall(t, tupleArgumentFactory+"; "+producer, "", "", "factory(pair())", "")
	}
	checkSourceStatsCall(t, "func factory(values ...int) s.DependencyStats { panic(0) }; func pair() {}", "", "", "factory(pair())", "")
	checkSourceStatsCall(t, "func factory(value int) s.DependencyStats { panic(0) }", "", "", "factory(unknown())", "")
}

func TestTupleArgumentsPreserveResultProvenance(t *testing.T) {
	for _, result := range []string{"s.DependencyStats", "*s.DependencyStats", "[]s.DependencyStats"} {
		receiver := "measured"
		if result == "[]s.DependencyStats" {
			receiver += "[0]"
		}
		declaration := "func factory(int, int) (" + result + ", error) { panic(0) }; " + tupleArgumentPair
		checkCollectionProvenance(t, declaration, collectionProvenanceUse{"unused string", "measured, _ := factory(pair())", receiver, false}, true)
	}
	declaration := "type Stats s.DependencyStats; func factory(int, int) Stats { panic(0) }; " + tupleArgumentPair
	checkSourceFunctionReportMapping(t, declaration, "measured := factory(pair())", "measured", false)
}

func TestNestedTupleArgumentsAndCycles(t *testing.T) {
	declaration := tupleArgumentFactory + "; func pair(first, second int) (int, int) { panic(0) }; func seed() (int, int) { panic(0) }"
	checkSourceStatsCall(t, declaration, "", "", "factory(pair(pair(seed())))", "s.DependencyStats")
	declaration = "func factory(value s.DependencyStats) s.DependencyStats { return value }; func seed() s.DependencyStats { panic(0) }"
	checkSourceStatsCall(t, declaration, "", "", "factory(factory(seed()))", "s.DependencyStats")
	for _, producer := range []string{
		"var pair = pair",
		"var pair = other; var other = pair",
		"var pair = wrap(pair())",
		"var pair = wrap(other()); var other = wrap(pair())",
		"var pair, err = split(pair())",
	} {
		declaration := tupleArgumentFactory + "; func wrap(int, int) func() (int, int) { panic(0) }; func split(int, int) (func() (int, int), error) { panic(0) }; " + producer
		checkSourceStatsCall(t, declaration, "", "", "factory(pair())", "")
	}
}

func TestScalarCallsAsFactoryArguments(t *testing.T) {
	declaration := "type Count int; type Counts[T any] []T; type Mapping[K comparable, V any] map[K]V; func factory(any) s.DependencyStats { panic(0) }"
	parameters := "values []int, value int, raw any, channel chan int, mapping map[int]int"
	for _, argument := range []string{
		"len(values)", "cap(values)", "int(value)", "Count(value)", "Counts[int](values)",
		"Mapping[int, int](mapping)", "func() int { return 1 }()",
		"make([]int, value)", "make(map[int]int)", "make(chan int, value)", "new(int)",
		"append(values, value)", "append(values, values...)", "copy(values, values)",
		"complex(1, 2)", "real(1i)", "imag(1i)", "min(1, 2)", "max(1, 2)", "recover()",
		"[]int(values)", "map[int]int(mapping)", "(chan int)(channel)", "(*int)(nil)",
		"(struct{})(struct{}{})", "(interface{})(raw)", "(func())(nil)",
	} {
		t.Run(argument, func(t *testing.T) {
			checkSourceStatsCall(t, declaration, parameters, "", "factory("+argument+")", "s.DependencyStats")
		})
	}
	checkCollectionProvenance(t, declaration, collectionProvenanceUse{"values []int", "measured := factory(len(values))", "measured", false}, true)
}

func TestScalarArgumentShapeGuards(t *testing.T) {
	declaration := "func factory(any) s.DependencyStats { panic(0) }; " + tupleArgumentPair
	for _, argument := range []string{
		"len()", "len(pair())", "int(pair())", "copy(nil)", "complex(1)", "recover(1)",
		"append()", "min()", "max()", "make()", "make([]int, 1, 2, 3)", "new()",
		"len(values...)", "append(values...)", "min(values...)",
		"close(channel)", "delete(mapping, 1)", "clear(values)", "panic(0)", "print(1)", "println(1)",
		"unknown()", "s.Unknown()", "(*callable)()",
	} {
		t.Run(argument, func(t *testing.T) {
			checkSourceStatsCall(t, declaration, "values []int, channel chan int, mapping map[int]int, callable *func()", "", "factory("+argument+")", "")
		})
	}
	checkSourceStatsCall(t, declaration, "", "len := func() (int, int) { panic(0) }", "factory(len())", "")
	checkSourceStatsCall(t, tupleArgumentFactory, "", "len := func() (int, int) { panic(0) }", "factory(len())", "s.DependencyStats")
}

func TestTupleArgumentsToScalarBuiltins(t *testing.T) {
	declaration := "func factory(any) s.DependencyStats { panic(0) }; " + tupleArgumentPair
	checkSourceStatsCall(t, declaration, "", "", "factory(min(pair()))", "s.DependencyStats")
	declaration += "; func parts() (float64, float64) { panic(0) }; func item() ([]int, int) { panic(0) }"
	checkSourceStatsCall(t, declaration, "", "", "factory(complex(parts()))", "s.DependencyStats")
	checkSourceStatsCall(t, declaration, "", "", "factory(append(item()))", "s.DependencyStats")
	checkSourceStatsCall(t, declaration, "", "", "factory(min(1, pair()))", "")
}

func TestIndirectCallableArguments(t *testing.T) {
	for _, parameter := range []string{"functions []func() (int, int)", "functions [1]func() (int, int)", "functions *[1]func() (int, int)", "functions map[int]func() (int, int)"} {
		checkSourceStatsCall(t, tupleArgumentFactory, parameter, "", "factory(functions[0]())", "s.DependencyStats")
	}
	checkSourceStatsCall(t, tupleArgumentFactory, "pointer *func() (int, int)", "", "factory((*pointer)())", "s.DependencyStats")
	checkSourceStatsCall(t, tupleArgumentFactory, "functions []func() int", "", "factory(functions[0](), functions[0]())", "s.DependencyStats")
	declaration := tupleArgumentFactory + "; func functions() []func() (int, int) { panic(0) }"
	checkSourceStatsCall(t, declaration, "", "", "factory(functions()[0]())", "s.DependencyStats")
	declaration = tupleArgumentFactory + "; func pointer() *func() (int, int) { panic(0) }"
	checkSourceStatsCall(t, declaration, "", "", "factory((*pointer())())", "s.DependencyStats")
	declaration = "type Stats s.DependencyStats; func factory(int, int) Stats { panic(0) }"
	checkSourceFunctionReportMapping(t, declaration, "var functions []func() (int, int); measured := factory(functions[0]())", "measured", false)
}

func TestIndirectCallableArgumentGuards(t *testing.T) {
	checkSourceStatsCall(t, tupleArgumentFactory, "functions []func() (int, int)", "", "factory(1, functions[0]())", "")
	checkSourceStatsCall(t, tupleArgumentFactory, "pointer *func(int) (int, int)", "", "factory((*pointer)())", "")
	for _, producer := range []string{
		"var functions = pair(); var pair = functions[0]",
		"var pointer = pair(); var pair = *pointer",
		"func wrap(int, int) []func() (int, int) { panic(0) }; var functions = wrap(pair()); var pair = functions[0]",
	} {
		checkSourceStatsCall(t, tupleArgumentFactory+"; "+producer, "", "", "factory(pair())", "")
	}
}

func TestNestedIndexedCallableArguments(t *testing.T) {
	declaration := "type F func() []F; func factory(any) s.DependencyStats { panic(0) }"
	expression := "factory(function" + strings.Repeat("()[0]", 26) + "())"
	checkSourceStatsCall(t, declaration, "function F", "", expression, "s.DependencyStats")
}
