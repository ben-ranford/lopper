package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

const packageReport = `return r.DependencyReport{Name:name, Language:"fixture",
UsedExportsCount:measured.UsedCount, TotalExportsCount:measured.TotalCount,
UsedPercent:measured.UsedPercent, TopUsedSymbols:measured.TopSymbols,
UsedImports:measured.UsedImports, UnusedImports:measured.UnusedImports}`

func TestPackageSourceProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, declaration, parameter, setup string
	}{
		{"function", "func stats() s.DependencyStats { return s.DependencyStats{} }", "", "measured := stats();"},
		{"alias", "type Stats = s.DependencyStats", ", measured Stats", ""},
		{"alias chain", "type Stats = Inner; type Inner = s.DependencyStats", ", measured Stats", ""},
		{"collection", "type Stats []s.DependencyStats", ", all Stats", "measured := all[0];"},
		{"field", "type Holder struct { Stats s.DependencyStats }", ", holder Holder", "measured := holder.Stats;"},
		{"embedded", "type Holder struct { s.DependencyStats }", ", holder Holder", "measured := holder.DependencyStats;"},
		{"function variable", "var stats = func() s.DependencyStats { return s.DependencyStats{} }", "", "measured := stats();"},
		{"interface method", "type Provider interface { Stats() s.DependencyStats }", ", p Provider", "measured := p.Stats();"},
		{"generic alias", "type GenericStats[T any] = s.DependencyStats", ", measured GenericStats[int]", ""},
		{"tuple results", "func stats() (error, s.DependencyStats) { panic(0) }", "", "_, measured := stats();"},
		{"source collection", "func stats() []s.DependencyStats { panic(0) }", "", "values := stats(); measured := values[0];"},
		{"collection assertion", "type Stats = s.DependencyStats", ", raw any", "values, ok := raw.([]Stats); _ = ok; measured := values[0];"},
		{"variadic parameter", "type Stats = s.DependencyStats", ", values ...Stats", "measured := values[0];"},
		{"nested collection", "type Matrix [][]s.DependencyStats", ", values Matrix", "measured := values[0][1];"},
		{"tuple arguments", "func pair() (int, string) { return 0, \"\" }; func stats(int, string) s.DependencyStats { panic(0) }", "", "measured := stats(pair());"},
		{"promoted fields", "type Holder struct { s.DependencyStats }", ", measured Holder", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkPackageReportCopy(t, tc.declaration, tc.parameter, tc.setup, packageReport)
		})
	}
}

func TestPackageProvenanceExcludesNonProductionDeclarations(t *testing.T) {
	for _, provider := range []string{"declarations_test.go", "testdata/declarations.go", "vendor/declarations.go"} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(root, provider), "package fixture; import s \"github.com/ben-ranford/lopper/internal/lang/shared\"; type Stats = s.DependencyStats")
			testutil.MustWriteFile(t, filepath.Join(root, "consumer.go"), "package fixture; import r \"github.com/ben-ranford/lopper/internal/report\"; func build(name string, measured Stats) r.DependencyReport { "+packageReport+" }")
			var output bytes.Buffer
			if code := run([]string{"-root", root}, &output, &output); code != 0 || output.Len() != 0 {
				t.Fatalf("non-production declaration supplied provenance: code=%d output=%s", code, &output)
			}
		})
	}
}

func TestConvertedReportCopies(t *testing.T) {
	for _, tc := range []struct{ name, report string }{
		{"name", strings.Replace(packageReport, "Name:name", "Name:string(name)", 1)},
		{"receiver", strings.ReplaceAll(packageReport, "measured.", "Stats(raw).")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkPackageReportCopy(t, "type Stats = s.DependencyStats; type Raw s.DependencyStats", ", measured Stats, raw Raw", "", tc.report)
		})
	}
}

func checkPackageReportCopy(t *testing.T, declaration, parameter, setup, report string) {
	t.Helper()
	root := t.TempDir()
	declarations := "package fixture\nimport s \"github.com/ben-ranford/lopper/internal/lang/shared\"\n" + declaration
	consumer := "package fixture\nimport r \"github.com/ben-ranford/lopper/internal/report\"\nfunc build(name string" + parameter + ") r.DependencyReport { " + setup + report + " }"
	testutil.MustWriteFile(t, filepath.Join(root, "declarations.go"), declarations)
	testutil.MustWriteFile(t, filepath.Join(root, "consumer.go"), consumer)
	var output bytes.Buffer
	if code := run([]string{"-root", root}, &output, &output); code != 1 || !strings.Contains(output.String(), "consumer.go:3: violation dependency-report-mapping in build") {
		t.Fatalf("code=%d output=%s", code, &output)
	}
}
