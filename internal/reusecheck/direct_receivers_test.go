package reusecheck

import (
	"strings"
	"testing"
)

func TestDirectStatsReceivers(t *testing.T) {
	for _, tc := range []struct {
		parameter, receiver string
		want                bool
	}{
		{"values []s.DependencyStats", "values[0]", true},
		{"values [2]*s.DependencyStats", "values[0]", true},
		{"values *[2]s.DependencyStats", "values[0]", true},
		{"values map[string]s.DependencyStats", `values["key"]`, true},
		{"values []s.DependencyStats, index int", "values[index]", true},
		{"values []*s.DependencyStats", "(*values[0])", true},
		{"source **s.DependencyStats", "(*source)", true},
		{"source ***s.DependencyStats", "(**source)", true},
		{"source **s.DependencyStats", "source", false},
		{"source s.DependencyStats", "(*source)", false},
		{"values []OtherStats", "values[0]", false},
		{"values []s.DependencyStats", "values[next()]", false},
		{"values func() []s.DependencyStats", "values()[0]", false},
	} {
		t.Run(tc.parameter+tc.receiver, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "measured s.DependencyStats", tc.parameter, 1)
			source = strings.ReplaceAll(source, "measured.", tc.receiver+".")
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || (len(findings) == 1 && !findings[0].Advisory) != tc.want || (!tc.want && len(findings) != 0) {
				t.Fatalf("findings=%+v err=%v want violation=%v", findings, err, tc.want)
			}
		})
	}
}

func TestDifferentIndexedStatsReceiversRemainAdvisory(t *testing.T) {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "values []s.DependencyStats", 1)
	source = strings.ReplaceAll(source, "measured.", "values[0].")
	source = strings.Replace(source, "values[0].UsedCount", "values[1].UsedCount", 1)
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 1 || !findings[0].Advisory {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
}

func TestAssertedStatsReceivers(t *testing.T) {
	for _, tc := range []struct {
		asserted, receiver string
		want               bool
	}{
		{"*s.DependencyStats", "measured", true},
		{"*s.DependencyStats", "(*measured)", true},
		{"**s.DependencyStats", "(*measured)", true},
		{"**s.DependencyStats", "measured", false},
		{"*s.DependencyStats", "(**measured)", false},
		{"s.DependencyStats", "(*measured)", false},
		{"*OtherStats", "(*measured)", false},
	} {
		t.Run(tc.asserted+tc.receiver, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "measured s.DependencyStats", "raw any", 1)
			source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", "measured := raw.("+tc.asserted+"); _ = s.BuildDependencyReportFromStats", 1)
			source = strings.ReplaceAll(source, "measured.", tc.receiver+".")
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || (len(findings) == 1 && !findings[0].Advisory) != tc.want || (!tc.want && len(findings) != 0) {
				t.Fatalf("findings=%+v err=%v want violation=%v", findings, err, tc.want)
			}
		})
	}
}
