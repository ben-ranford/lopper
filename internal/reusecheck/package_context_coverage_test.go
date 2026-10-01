package reusecheck

import (
	"fmt"
	"strings"
	"testing"
)

func TestPackageContextCoverageUnitesProviders(t *testing.T) {
	for _, platforms := range [][]string{{"linux", "windows"}, {"linux", "windows", "darwin"}} {
		t.Run(strings.Join(platforms, "+"), func(t *testing.T) {
			sources := map[string][]byte{
				"consumer.go": contextCoverageSource(strings.Join(platforms, " || "), packageMappingSource("measured Stats", "", "measured")),
			}
			for index, platform := range platforms {
				alias := fmt.Sprintf("source%d", index)
				sources["types_"+platform+".go"] = []byte(contextCoverageProvider(alias, sharedPackage, "type Stats = "+alias+".DependencyStats"))
			}
			assertContextCoverage(t, sources, 1, false)
		})
	}
}

func TestPackageContextCoverageRetainsImportOwnership(t *testing.T) {
	for _, importClause := range []string{"r", "."} {
		typeName := "r.DependencyStats"
		if importClause == "." {
			typeName = "DependencyStats"
		}
		sources := map[string][]byte{
			"consumer.go":      contextCoverageSource("linux || windows", packageMappingSource("measured Stats", "", "measured")),
			"types_linux.go":   []byte(contextCoverageProvider(importClause, sharedPackage, "type Stats = "+typeName)),
			"types_windows.go": []byte(contextCoverageProvider("other", sharedPackage, "type Stats = other.DependencyStats")),
		}
		assertContextCoverage(t, sources, 1, false)
		sources["types_linux.go"] = []byte(contextCoverageProvider(importClause, "example.com/shared", "type Stats = "+typeName))
		assertContextCoverage(t, sources, 0, false)
	}
}

func TestPackageContextCoverageRequiresEveryBuild(t *testing.T) {
	for _, tc := range []struct {
		name, predicate, second, header string
	}{
		{"missing platform", "linux || windows || darwin", "type Stats = s.DependencyStats", ""},
		{"different type", "linux || windows", "type Stats struct{}", ""},
		{"defined type", "linux || windows", "type Stats s.DependencyStats", ""},
		{"custom gap", "linux || windows", "type Stats = s.DependencyStats", "custom"},
		{"unconstrained consumer", "custom || !custom", "type Stats = s.DependencyStats", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := map[string][]byte{
				"consumer.go":      contextCoverageSource(tc.predicate, packageMappingSource("measured Stats", "", "measured")),
				"types_linux.go":   []byte(contextCoverageProvider("s", sharedPackage, "type Stats = s.DependencyStats")),
				"types_windows.go": contextCoverageSource(tc.header, contextCoverageProvider("s", sharedPackage, tc.second)),
			}
			assertContextCoverage(t, sources, 0, false)
		})
	}
}

func TestPackageContextCoverageUnitesCallResults(t *testing.T) {
	for _, tc := range []struct {
		name, parameter, setup, linux, windows string
	}{
		{"factory", "unused string", "measured := factory()", "func factory() s.DependencyStats { panic(0) }", "func factory() other.DependencyStats { panic(0) }"},
		{"method", "holder Holder", "measured := holder.stats()", "func (Holder) stats() s.DependencyStats { panic(0) }", "func (Holder) stats() other.DependencyStats { panic(0) }"},
		{"alias receiver", "holder Holder", "measured := holder.stats()", "type Alias = Holder; func (Alias) stats() s.DependencyStats { panic(0) }", "func (Holder) stats() other.DependencyStats { panic(0) }"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := map[string][]byte{
				"common.go":        []byte("package fixture; type Holder struct{}"),
				"consumer.go":      contextCoverageSource("linux || windows", packageMappingSource(tc.parameter, tc.setup, "measured")),
				"calls_linux.go":   []byte(contextCoverageProvider("s", sharedPackage, tc.linux)),
				"calls_windows.go": []byte(contextCoverageProvider("other", sharedPackage, tc.windows)),
			}
			assertContextCoverage(t, sources, 1, false)
			sources["calls_windows.go"] = []byte(contextCoverageProvider("other", "example.com/shared", tc.windows))
			assertContextCoverage(t, sources, 0, false)
		})
	}
}

func TestPackageContextCoverageCombinesPlatformAxes(t *testing.T) {
	sources := map[string][]byte{
		"consumer.go":       contextCoverageSource("(linux || windows) && (amd64 || arm64)", packageMappingSource("measured Stats", "", "measured")),
		"types_amd64.go":    []byte("package fixture; type Stats = Counts"),
		"types_arm64.go":    []byte("package fixture; type Stats = Counts"),
		"counts_linux.go":   []byte(contextCoverageProvider("s", sharedPackage, "type Counts = s.DependencyStats")),
		"counts_windows.go": []byte(contextCoverageProvider("other", sharedPackage, "type Counts = other.DependencyStats")),
	}
	assertContextCoverage(t, sources, 1, false)
	sources["counts_windows.go"] = []byte(contextCoverageProvider("other", "example.com/shared", "type Counts = other.DependencyStats"))
	assertContextCoverage(t, sources, 0, false)
}

func TestPackageContextCoverageUnitesCustomPredicates(t *testing.T) {
	for _, tc := range []struct{ consumer, first, second string }{
		{"linux", "linux && custom", "linux && !custom"},
		{"linux && (red || blue)", "linux && red && !blue", "linux && blue"},
	} {
		sources := map[string][]byte{
			"consumer.go": contextCoverageSource(tc.consumer, packageMappingSource("measured Stats", "", "measured")),
			"first.go":    contextCoverageSource(tc.first, contextCoverageProvider("s", sharedPackage, "type Stats = s.DependencyStats")),
			"second.go":   contextCoverageSource(tc.second, contextCoverageProvider("other", sharedPackage, "type Stats = other.DependencyStats")),
		}
		assertContextCoverage(t, sources, 1, false)
	}
}

func TestPackageContextCoverageAgreesOnSeverity(t *testing.T) {
	for _, tc := range []struct {
		linux, windows string
		advisory       bool
	}{
		{"", "", false},
		{"", "; func (Stats) UsedCount() int { return 0 }", true},
		{"; func (Stats) UsedCount() int { return 0 }", "", true},
		{"; func (Stats) UsedCount() int { return 0 }", "; func (Stats) UsedCount() int { return 0 }", true},
	} {
		sources := map[string][]byte{
			"consumer.go":      contextCoverageSource("linux || windows", packageMappingSource("measured Stats", "", "measured")),
			"types_linux.go":   []byte(contextCoverageProvider("s", sharedPackage, "type Stats struct { s.DependencyStats }"+tc.linux)),
			"types_windows.go": []byte(contextCoverageProvider("other", sharedPackage, "type Stats struct { other.DependencyStats }"+tc.windows)),
		}
		assertContextCoverage(t, sources, 1, tc.advisory)
	}
}

func TestPackageContextCoverageSeparatesSiblingLiterals(t *testing.T) {
	for _, tc := range []struct {
		predicate string
		count     int
	}{
		{"linux || windows", 0},
		{"linux", 1},
		{"windows", 1},
	} {
		sources := map[string][]byte{
			"consumer.go":      contextCoverageSource(tc.predicate, contextCoverageSiblingLiterals()),
			"types_linux.go":   []byte(contextCoverageProvider("s", sharedPackage, "type First = s.DependencyStats; type Second struct{}")),
			"types_windows.go": []byte(contextCoverageProvider("s", sharedPackage, "type Second = s.DependencyStats; type First struct{}")),
		}
		assertContextCoverage(t, sources, tc.count, false)
	}
	sources := map[string][]byte{
		"consumer.go":      contextCoverageSource("linux || windows", contextCoverageSiblingLiterals()),
		"types_linux.go":   []byte(contextCoverageProvider("s", sharedPackage, "type First = s.DependencyStats; type Second = s.DependencyStats")),
		"types_windows.go": []byte(contextCoverageProvider("s", sharedPackage, "type First = s.DependencyStats; type Second = s.DependencyStats")),
	}
	assertContextCoverage(t, sources, 2, false)
}

func TestPackageContextCoveragePreservesUnprovenBaseline(t *testing.T) {
	for _, header := range []string{"linux && !linux", "("} {
		assertContextCoverage(t, map[string][]byte{
			"consumer.go": contextCoverageSource(header, mappingFixture),
		}, 1, false)
	}
	findings, err := Analyze("_consumer.go", []byte(mappingFixture))
	if err != nil || len(findings) != 1 || findings[0].Advisory {
		t.Fatalf("unknown filename metadata removed direct source findings: %+v error=%v", findings, err)
	}
}

func TestPackageContextCoverageLeavesJointCustomAxesUnknown(t *testing.T) {
	sources := map[string][]byte{
		"consumer.go": contextCoverageSource("linux", packageMappingSource("measured Stats", "", "measured")),
	}
	for index, tag := range []string{"feature", "!feature", "debug", "!debug"} {
		declaration := "package fixture; type Stats = Counts"
		if index >= 2 {
			declaration = contextCoverageProvider("s", sharedPackage, "type Counts = s.DependencyStats")
		}
		sources[fmt.Sprintf("part%d.go", index)] = contextCoverageSource(tag, declaration)
	}
	assertContextCoverage(t, sources, 0, false)
}

func contextCoverageSiblingLiterals() string {
	_, body, _ := strings.Cut(mappingFixture, "return ")
	literal := strings.TrimSuffix(body, "\n}")
	literal = strings.Join(strings.Fields(literal), " ")
	first := strings.ReplaceAll(literal, "measured.", "first.")
	second := strings.ReplaceAll(literal, "measured.", "second.")
	return `package fixture; import r "` + module + `report"; func build(name string, first First, second Second) { _ = ` + first + "; _ = " + second + " }"
}

func contextCoverageSource(predicate, source string) []byte {
	if predicate != "" {
		source = "//go:build " + predicate + "\n\n" + source
	}
	return []byte(source)
}

func contextCoverageProvider(alias, path, declarations string) string {
	return "package fixture; import " + alias + " \"" + path + "\"; " + declarations
}

func assertContextCoverage(t *testing.T, sources map[string][]byte, count int, advisory bool) {
	t.Helper()
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != count {
		t.Fatalf("coverage findings=%+v error=%v, want %d findings", findings, err, count)
	}
	for _, finding := range findings {
		if finding.Path != "consumer.go" || finding.Rule != "dependency-report-mapping" || finding.Advisory != advisory {
			t.Fatalf("coverage changed source or severity: %+v, want advisory=%v", finding, advisory)
		}
	}
}
