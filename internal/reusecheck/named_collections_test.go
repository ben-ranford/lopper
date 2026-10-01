package reusecheck

import (
	"strings"
	"testing"
)

func TestNamedCollectionStatsProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, declarations, parameter, setup, receiver string
		loop, want                                     bool
	}{
		{"slice range", "type Collection []s.DependencyStats", "values Collection", "for _, measured := range values {", "measured", true, true},
		{"array range", "type Collection [2]s.DependencyStats", "values Collection", "for _, measured := range values {", "measured", true, true},
		{"pointer array range", "type Collection [2]s.DependencyStats", "values *Collection", "for _, measured := range values {", "measured", true, true},
		{"map range", "type Collection map[string]s.DependencyStats", "values Collection", "for _, measured := range values {", "measured", true, true},
		{"channel range", "type Collection chan s.DependencyStats", "values Collection", "for measured := range values {", "measured", true, true},
		{"iterator range", "type Collection func(func(s.DependencyStats) bool)", "values Collection", "for measured := range values {", "measured", true, true},
		{"named iterator callback", "type Yield func(s.DependencyStats) bool; type Collection func(Yield)", "values Collection", "for measured := range values {", "measured", true, true},
		{"named chain", "type Original []s.DependencyStats; type Second Original; type Collection = Second", "values Collection", "for _, measured := range values {", "measured", true, true},
		{"slice index", "type Collection []s.DependencyStats", "values Collection", "", "values[0]", false, true},
		{"array index", "type Collection [2]s.DependencyStats", "values Collection", "", "values[0]", false, true},
		{"pointer array index", "type Collection [2]s.DependencyStats", "values *Collection", "", "values[0]", false, true},
		{"map index", "type Collection map[string]s.DependencyStats", "values Collection", "", `values["key"]`, false, true},
		{"map comma ok", "type Collection map[string]s.DependencyStats", "values Collection", `measured, ok := values["key"]; _ = ok`, "measured", false, true},
		{"receive", "type Collection <-chan s.DependencyStats", "values Collection", "measured := <-values", "measured", false, true},
		{"receive comma ok", "type Collection chan s.DependencyStats", "values Collection", "measured, ok := <-values; _ = ok", "measured", false, true},
		{"slice operation", "type Collection []s.DependencyStats", "values Collection", "copied := values[:]", "copied[0]", false, true},
		{"pointer array slice", "type Collection [2]s.DependencyStats", "values *Collection", "copied := values[:]", "copied[0]", false, true},
		{"append", "type Collection []s.DependencyStats", "values Collection", "copied := append(values, s.DependencyStats{})", "copied[0]", false, true},
		{"conversion", "type Collection []s.DependencyStats", "raw []s.DependencyStats", "values := Collection(raw)", "values[0]", false, true},
		{"allocation", "type Collection []s.DependencyStats", "unused string", "values := make(Collection, 2)", "values[0]", false, true},
		{"address array", "type Collection [2]s.DependencyStats", "values Collection", "for _, measured := range &values {", "measured", true, true},
		{"dereference array", "type Collection [2]s.DependencyStats", "values *Collection", "for _, measured := range *values {", "measured", true, true},
		{"aliased stats", "type Stats = s.DependencyStats; type Collection []Stats", "values Collection", "", "values[0]", false, true},
		{"distinct stats index", "type Stats s.DependencyStats; type Collection []Stats", "values Collection", "", "values[0]", false, false},
		{"distinct stats range", "type Stats s.DependencyStats; type Collection []Stats", "values Collection", "for _, measured := range values {", "measured", true, false},
		{"distinct stats receive", "type Stats s.DependencyStats; type Collection chan Stats", "values Collection", "measured := <-values", "measured", false, false},
		{"distinct iterator stats", "type Stats s.DependencyStats; type Yield func(Stats) bool; type Collection func(Yield)", "values Collection", "for measured := range values {", "measured", true, false},
		{"cyclic iterator callback", "type Yield Yield; type Collection func(Yield)", "values Collection", "for measured := range values {", "measured", true, false},
		{"distinct stats address", "type Stats s.DependencyStats", "measured Stats", "", "(&measured)", false, false},
		{"pointer slice range", "type Collection []s.DependencyStats", "values *Collection", "for _, measured := range values {", "measured", true, false},
		{"pointer map index", "type Collection map[string]s.DependencyStats", "values *Collection", "", `values["key"]`, false, false},
		{"send only channel", "type Collection chan<- s.DependencyStats", "values Collection", "measured := <-values", "measured", false, false},
		{"type cycle", "type Collection Collection", "values Collection", "", "values[0]", false, false},
		{"mutual type cycle", "type Collection Other; type Other Collection", "values Collection", "for _, measured := range values {", "measured", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "measured s.DependencyStats", tc.parameter, 1)
			source = strings.Replace(source, `_ = s.BuildDependencyReportFromStats(name, "python", measured)`, tc.setup, 1)
			source = strings.ReplaceAll(source, "measured.", tc.receiver+".")
			if tc.loop {
				source += "; return r.DependencyReport{} }"
			}
			source += "\n" + tc.declarations
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || (len(findings) == 1 && !findings[0].Advisory) != tc.want || (!tc.want && len(findings) != 0) {
				t.Fatalf("findings=%+v err=%v want violation=%v", findings, err, tc.want)
			}
		})
	}
}
