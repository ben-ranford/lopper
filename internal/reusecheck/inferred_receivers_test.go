package reusecheck

import (
	"strings"
	"testing"
)

func TestInferredFactoryStatsReceivers(t *testing.T) {
	for _, tc := range []struct {
		name, declaration, receiver string
		want                        bool
	}{
		{"factory value", "measured := s.BuildDependencyStats(name, nil, nil)", "(&measured)", true},
		{"factory variable", "var measured = s.BuildDependencyStats(name, nil, nil)", "(&measured)", true},
		{"factory alias", "first := s.BuildDependencyStats(name, nil, nil); measured := first", "(&measured)", true},
		{"dereferenced alias", "first := s.BuildDependencyStats(name, nil, nil); pointer := &first; measured := *pointer", "(&measured)", true},
		{"pointer alias", "first := s.BuildDependencyStats(name, nil, nil); measured := &first", "(&(*measured))", true},
		{"pointer depth", "first := s.BuildDependencyStats(name, nil, nil); measured := &first", "(&measured)", false},
		{"value dereference", "measured := s.BuildDependencyStats(name, nil, nil)", "(*measured)", false},
		{"wrong factory", "measured := s.UnknownFactory(name, nil, nil)", "(&measured)", false},
		{"wrong arity", "measured := s.BuildDependencyStats(name, nil)", "(&measured)", false},
		{"cyclic initializer", "var measured = measured", "(&measured)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "measured s.DependencyStats", "unused string", 1)
			source = strings.Replace(source, `_ = s.BuildDependencyReportFromStats(name, "python", measured)`, tc.declaration, 1)
			source = strings.ReplaceAll(source, "measured.", tc.receiver+".")
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || (len(findings) == 1 && !findings[0].Advisory) != tc.want || (!tc.want && len(findings) != 0) {
				t.Fatalf("findings=%+v err=%v want violation=%v", findings, err, tc.want)
			}
		})
	}
}

func TestInferredFactoryStatsReceiverDotImport(t *testing.T) {
	source := strings.Replace(mappingFixture, "measured s.DependencyStats", "unused string", 1)
	source = strings.Replace(source, `_ = s.BuildDependencyReportFromStats(name, "python", measured)`, "measured := (s.BuildDependencyStats)(name, nil, nil)", 1)
	source = strings.ReplaceAll(source, "measured.", "(&measured).")
	source = strings.Replace(source, `import s "`, `import . "`, 1)
	source = strings.ReplaceAll(source, "s.", "")
	findings, err := Analyze("fixture.go", []byte(source))
	if err != nil || len(findings) != 1 || findings[0].Advisory {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
}

func TestInferredRangeStatsReceivers(t *testing.T) {
	for _, tc := range []struct {
		name, collection, binding, declaration, receiver string
		want                                             bool
	}{
		{"slice value", "[]s.DependencyStats", "_, measured", "", "(&measured)", true},
		{"map value", "map[string]s.DependencyStats", "_, measured", "", "(&measured)", true},
		{"channel value", "chan s.DependencyStats", "measured", "", "(&measured)", true},
		{"range alias", "[]s.DependencyStats", "_, first", "measured := first;", "(&measured)", true},
		{"pointer value", "[]*s.DependencyStats", "_, measured", "", "(&(*measured))", true},
		{"pointer depth", "[]*s.DependencyStats", "_, measured", "", "(&measured)", false},
		{"slice index", "[]s.DependencyStats", "measured", "", "(&measured)", false},
		{"unrelated type", "[]OtherStats", "_, measured", "", "(&measured)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := strings.Replace(mappingFixture, "measured s.DependencyStats", "values "+tc.collection, 1)
			source = strings.Replace(source, `_ = s.BuildDependencyReportFromStats(name, "python", measured)`, "for "+tc.binding+" := range values { "+tc.declaration, 1)
			source = strings.Replace(source, "EstimatedUnusedBytes:0}\n}", "EstimatedUnusedBytes:0}\n}\nreturn r.DependencyReport{}\n}", 1)
			source = strings.ReplaceAll(source, "measured.", tc.receiver+".")
			findings, err := Analyze("fixture.go", []byte(source))
			if err != nil || (len(findings) == 1 && !findings[0].Advisory) != tc.want || (!tc.want && len(findings) != 0) {
				t.Fatalf("findings=%+v err=%v want violation=%v", findings, err, tc.want)
			}
		})
	}
}
