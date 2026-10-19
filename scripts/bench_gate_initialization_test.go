package scripts

import "testing"

func TestBenchGateSeparatesInitializationPackages(t *testing.T) {
	for _, scenario := range []string{"internal to external", "external to internal", "transitive only", "other package", "first production edge", "last production edge"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newBenchGateFixture(t, "benchpkg")
			base, head := initializationBoundaryFiles(scenario)
			fixture.writeBenchmarkPackage("benchpkg", base)
			fixture.commit("base")
			fixture.writeBenchmarkPackage("benchpkg", head)
			fixture.commit("head")
			assertBenchGateHarnessMismatch(t, fixture)
		})
	}
}

func initializationBoundaryFiles(scenario string) (map[string]string, map[string]string) {
	base := benchmarkHarnessPackageFiles(map[string]string{"setup/setup.go": "package setup\nvar Value = 1\n"})
	head := map[string]string{"added_test.go": comparabilityOrdinaryFile("added", false)}
	switch scenario {
	case "internal to external":
		base["ordinary_test.go"] = comparabilityOrdinaryFile("", false)
		head["added_test.go"] = externalInitializationTest()
	case "external to internal":
		base["external_test.go"] = externalInitializationTest()
	case "transitive only":
		base["indirect/indirect.go"] = "package indirect\nimport \"" + comparabilitySetupPath + "\"\nvar Value = setup.Value\n"
		base["ordinary_test.go"] = "package benchpkg\nimport(\"testing\"; \"github.com/ben-ranford/lopper/benchpkg/indirect\")\nfunc TestIndirect(t *testing.T){if indirect.Value != 1 {t.Fatal(indirect.Value)}}\n"
	case "other package":
		base["other/other.go"] = "package other\nimport \"" + comparabilitySetupPath + "\"\nvar Value = setup.Value\n"
	case "first production edge":
		head = map[string]string{"production.go": "package benchpkg\nimport \"" + comparabilitySetupPath + "\"\nvar Value = setup.Value\n"}
	case "last production edge":
		base["production.go"] = "package benchpkg\nimport \"" + comparabilitySetupPath + "\"\nvar Value = setup.Value\n"
		head = map[string]string{"production.go": "package benchpkg\nvar Value = 1\n"}
	}
	return base, head
}

func externalInitializationTest() string {
	return "package benchpkg_test\nimport(\"testing\"; sitter \"" + comparabilitySetupPath + "\")\nfunc TestExternal(t *testing.T){if sitter.Value != 1 {t.Fatal(sitter.Value)}}\n"
}

func TestBenchGateUsesBuildSelectedProductionImports(t *testing.T) {
	fixture := newBenchGateFixture(t, "benchpkg")
	fixture.writeBenchmarkPackage("benchpkg", benchmarkHarnessPackageFiles(nil))
	fixture.commit("base")
	fixture.writeBenchmarkPackage("benchpkg", map[string]string{
		"excluded.go": "//go:build comparability_excluded\n\npackage benchpkg\nimport _ \"does.not.exist/registration\"\n",
	})
	fixture.commit("head")
	output, code := fixture.runBenchGate()
	if code != 0 {
		t.Fatalf("build-excluded imports must not join normal inventory: code=%d\n%s", code, output)
	}
}

func TestBenchGateRetainsExactImportRoots(t *testing.T) {
	for _, kind := range []string{"blank", "dot", "selected helper alias"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newBenchGateFixture(t, "benchpkg")
			base, head := exactImportRootFiles(kind)
			fixture.writeBenchmarkPackage("benchpkg", base)
			fixture.commit("base")
			fixture.writeBenchmarkPackage("benchpkg", head)
			fixture.commit("head")
			assertBenchGateHarnessMismatch(t, fixture)
		})
	}
}

func exactImportRootFiles(kind string) (map[string]string, map[string]string) {
	base, head := comparableImportFiles("duplicate alias")
	switch kind {
	case "blank":
		head["added_test.go"] = "package benchpkg\nimport _ \"" + comparabilitySetupPath + "\"\n"
	case "dot":
		head["added_test.go"] = "package benchpkg\nimport(\"testing\"; . \"" + comparabilitySetupPath + "\")\nfunc TestAdded(t *testing.T){if Value != 1 {t.Fatal(Value)}}\n"
	case "selected helper alias":
		base["bench_test.go"] = "package benchpkg\nimport \"testing\"\nfunc BenchmarkValue(b *testing.B){if fixtureValue()!=1 {b.Fatal(\"setup\")}}\n"
		base["helper_test.go"] = "package benchpkg\nimport sitter \"" + comparabilitySetupPath + "\"\nfunc fixtureValue()int{return sitter.Value}\n"
	}
	return base, head
}
