package sharedcontract_test

import (
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

// The builder preserves collection shape and complete import locations, including
// the empty-but-non-nil slices produced by BuildDependencyStats for no imports.
func TestDependencyReportStatsCollectionContract(t *testing.T) {
	normalize := func(s string) string { return s }
	files := []shared.FileUsage{{Imports: []shared.ImportRecord{
		{Dependency: "dep", Module: "dep/api", Name: "used", Local: "used", Location: report.Location{File: "source", Line: 2, Column: 3}},
		{Dependency: "dep", Module: "dep/api", Name: "unused", Local: "unused"},
		{Dependency: "dep", Module: "dep", Name: "*", Local: "all", Wildcard: true},
	}, Usage: map[string]int{"used": 4}}}
	tests := []struct {
		name  string
		stats shared.DependencyStats
		want  report.DependencyReport
	}{
		{name: "nil collections", want: report.DependencyReport{Name: "dep", Language: "test"}},
		{name: "no imports", stats: shared.BuildDependencyStats("dep", nil, normalize), want: report.DependencyReport{
			Name: "dep", Language: "test", TopUsedSymbols: []report.SymbolUsage{}, UsedImports: []report.ImportUse{}, UnusedImports: []report.ImportUse{},
		}},
		{name: "used unused and wildcard imports", stats: shared.BuildDependencyStats("dep", files, normalize), want: report.DependencyReport{
			Name: "dep", Language: "test", UsedExportsCount: 2, TotalExportsCount: 3, UsedPercent: float64(2) / 3 * 100,
			TopUsedSymbols: []report.SymbolUsage{{Name: "used", Count: 4}, {Name: "*", Count: 1}},
			UsedImports: []report.ImportUse{
				{Name: "*", Module: "dep", Locations: []report.Location{{}}},
				{Name: "used", Module: "dep/api", Locations: []report.Location{{File: "source", Line: 2, Column: 3}}},
			},
			UnusedImports: []report.ImportUse{{Name: "unused", Module: "dep/api", Locations: []report.Location{{}}}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shared.BuildDependencyReportFromStats("dep", "test", tt.stats)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("report mismatch: got %#v, want %#v", got, tt.want)
			}
		})
	}
}
