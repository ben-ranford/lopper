package scripts

import (
	"testing"
)

const comparabilitySetupPath = "github.com/ben-ranford/lopper/benchpkg/setup"

func TestBenchGateReusesInitializedImportEdges(t *testing.T) {
	for _, kind := range []string{"duplicate alias", "production edge", "reflect import only"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newBenchGateFixture(t, "benchpkg")
			base, head := comparableImportFiles(kind)
			fixture.writeBenchmarkPackage("benchpkg", base)
			fixture.commit("base")
			fixture.writeBenchmarkPackage("benchpkg", head)
			fixture.commit("head")
			output, code := fixture.runBenchGate()
			if code != 0 {
				t.Fatalf("same initialized imports must compare: code=%d\n%s", code, output)
			}
		})
	}
}

func comparableImportFiles(kind string) (map[string]string, map[string]string) {
	base := benchmarkHarnessPackageFiles(map[string]string{
		"setup/setup.go":   "package setup\nvar Value = 1\n",
		"ordinary_test.go": comparabilityOrdinaryFile("", false),
	})
	head := map[string]string{"added_test.go": comparabilityOrdinaryFile("other", false)}
	if kind == "production edge" {
		delete(base, "ordinary_test.go")
		base["production.go"] = "package benchpkg\nimport \"" + comparabilitySetupPath + "\"\nvar Production = setup.Value\n"
	}
	if kind == "reflect import only" {
		head = map[string]string{"ordinary_test.go": comparabilityOrdinaryFile("", true)}
	}
	return base, head
}

func comparabilityOrdinaryFile(suffix string, reflection bool) string {
	imports := "\"testing\"; sitter \"" + comparabilitySetupPath + "\""
	body := "if sitter.Value != 1 { t.Fatal(sitter.Value) }"
	if reflection {
		imports += "; \"reflect\""
		body = "if !reflect.DeepEqual(sitter.Value,1) { t.Fatal(sitter.Value) }"
	}
	return "package benchpkg\nimport(" + imports + ")\nfunc TestOrdinary" + suffix + "(t *testing.T){" + body + "}\n"
}

func TestBenchGateProtectsDefaultImportBindings(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	base := defaultBindingFiles("math/rand", "math/rand/v2")
	fixture.writeBenchmarkPackage("benchpkg", base)
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchpkg", defaultBindingFiles("math/rand/v2", "math/rand"))
	fixture.commit("head")
	assertBenchGateHarnessMismatch(t, fixture)
}

func defaultBindingFiles(benchmarkPath, ordinaryPath string) map[string]string {
	return map[string]string{
		"bench_test.go":    "package benchpkg\nimport \"testing\"\nimport \"" + benchmarkPath + "\"\nvar sink int\nfunc BenchmarkValue(b *testing.B){for i:=0;i<b.N;i++ {sink=rand.Int()}}\n",
		"ordinary_test.go": "package benchpkg\nimport(\"testing\"; \"" + ordinaryPath + "\")\nfunc TestOrdinary(t *testing.T){_ = rand.Int()}\n",
	}
}
