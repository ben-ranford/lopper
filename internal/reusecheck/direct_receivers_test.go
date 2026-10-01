package reusecheck

import (
	"strings"
	"testing"
)

func TestDirectStatsReceivers(t *testing.T) {
	checkDirectStatsReceivers(t, []statsReceiverCase{
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
	})
}

func TestDifferentIndexedStatsReceiversRemainAdvisory(t *testing.T) {
	for _, indices := range [][2]string{
		{"0", "1"}, {"i + 1", "i - 1"}, {"i + j", "j + i"},
		{"-i", "+i"}, {"i + 1", "j + 1"}, {"(i + j) * 2", "i + (j * 2)"},
	} {
		source := strings.Replace(mappingFixture, "measured s.DependencyStats", "values []s.DependencyStats, i, j int", 1)
		first, second := "values["+indices[0]+"]", "values["+indices[1]+"]"
		source = strings.ReplaceAll(source, "measured.", first+".")
		source = strings.Replace(source, first+".UsedCount", second+".UsedCount", 1)
		findings, err := Analyze("fixture.go", []byte(source))
		if err != nil || len(findings) != 1 || !findings[0].Advisory {
			t.Fatalf("indices=%v findings=%+v err=%v", indices, findings, err)
		}
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

func TestStructFieldStatsReceivers(t *testing.T) {
	for _, tc := range []struct {
		declaration, parameter, receiver string
		want                             bool
	}{
		{"type Holder struct { stats s.DependencyStats }", "h Holder", "h.stats", true},
		{"type Holder struct { stats *s.DependencyStats }", "h *Holder", "h.stats", true},
		{"type Stats = s.DependencyStats; type Holder struct { stats Stats }", "h Holder", "h.stats", true},
		{"type Holder struct { stats s.DependencyStats }; type Outer struct { inner Holder }", "h Outer", "h.inner.stats", true},
		{"type Holder struct { stats s.DependencyStats }", "h []Holder", "h[0].stats", true},
		{"type Holder struct { stats s.DependencyStats }; type Outer struct { Holder }", "h Outer", "h.stats", true},
		{"", "h struct { stats s.DependencyStats }", "h.stats", true},
		{"type Holder struct { s.DependencyStats }", "h Holder", "h.DependencyStats", true},
		{"type Holder struct { *s.DependencyStats }", "h Holder", "h.DependencyStats", true},
		{"type Stats = s.DependencyStats; type Holder struct { Stats }", "h Holder", "h.Stats", true},
		{"type Stats = s.DependencyStats; type Holder struct { *Stats }", "h Holder", "h.Stats", true},
		{"type Stats s.DependencyStats; type Holder struct { Stats }", "h Holder", "h.Stats", false},
		{"", "h struct { s.DependencyStats }", "h.DependencyStats", true},
		{"", "h s.UnknownHolder", "h.stats", false},
		{"type Holder struct { stats OtherStats }", "h Holder", "h.stats", false},
		{"type Stats s.DependencyStats; type Holder struct { stats Stats }", "h Holder", "h.stats", false},
		{"type Holder struct { stats s.DependencyStats }", "h func() Holder", "h().stats", false},
	} {
		t.Run(tc.parameter+tc.receiver+tc.declaration, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "measured s.DependencyStats", tc.parameter, 1)
			source = strings.ReplaceAll(source, "measured.", tc.receiver+".")
			source += "\n" + tc.declaration
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || (len(findings) == 1 && !findings[0].Advisory) != tc.want || (!tc.want && len(findings) != 0) {
				t.Fatalf("findings=%+v err=%v want violation=%v", findings, err, tc.want)
			}
		})
	}
}

func TestStructFieldStatsAlias(t *testing.T) {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "h struct { stats s.DependencyStats }", 1)
	source = strings.Replace(source, "_ = s.BuildDependencyReportFromStats", "measured := h.stats; _ = s.BuildDependencyReportFromStats", 1)
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 1 || findings[0].Advisory {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
}

func TestDifferentStructFieldStatsReceiversRemainAdvisory(t *testing.T) {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "h struct { first, second s.DependencyStats }", 1)
	source = strings.ReplaceAll(source, "measured.", "h.first.")
	source = strings.Replace(source, "h.first.UsedCount", "h.second.UsedCount", 1)
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 1 || !findings[0].Advisory {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
}

type statsReceiverCase struct {
	parameter, receiver string
	want                bool
}

func checkDirectStatsReceivers(t *testing.T, cases []statsReceiverCase) {
	t.Helper()
	for _, tc := range cases {
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
