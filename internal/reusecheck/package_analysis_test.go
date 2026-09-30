package reusecheck

import (
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeSourcesSeparatesPackageScopes(t *testing.T) {
	provider := "package fixture; import s \"" + sharedPackage + "\"; type Stats = s.DependencyStats"
	consumer := packageMappingSource("measured Stats", "", "measured")
	for _, tc := range []struct {
		name, path, source string
		want               int
	}{
		{"same package", "adapter/definitions.go", provider, 1},
		{"neighbor directory", "other/definitions.go", provider, 0},
		{"different package", "adapter/definitions.go", strings.Replace(provider, "package fixture", "package other", 1), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			findings, err := AnalyzeSources(map[string][]byte{
				tc.path:               []byte(tc.source),
				"adapter/consumer.go": []byte(consumer),
			})
			if err != nil || len(findings) != tc.want {
				t.Fatalf("findings=%+v err=%v", findings, err)
			}
		})
	}
}

func TestAnalyzeSourcesOrdersOriginalLocations(t *testing.T) {
	_, body, _ := strings.Cut(mappingFixture, "\nfunc build")
	second := "func another" + body
	sources := map[string][]byte{
		"z/last.go":  []byte(mappingFixture),
		"a/first.go": []byte(mappingFixture + "\n" + second),
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 3 {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
	paths := []string{findings[0].Path, findings[1].Path, findings[2].Path}
	if !reflect.DeepEqual(paths, []string{"a/first.go", "a/first.go", "z/last.go"}) || findings[0].Line >= findings[1].Line || findings[1].Function != "another" {
		t.Fatalf("unstable source locations: %+v", findings)
	}
	sources["invalid.go"] = []byte("package invalid; func broken")
	if findings, err := AnalyzeSources(sources); err == nil || len(findings) != 0 {
		t.Fatalf("parse failure returned partial findings: %+v err=%v", findings, err)
	}
}

func TestAnalyzeSourcesEmptyInput(t *testing.T) {
	if findings, err := AnalyzeSources(nil); err != nil || len(findings) != 0 {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
}

func TestAnalyzeSourcesDoesNotChooseConflictingBuildDeclarations(t *testing.T) {
	consumer := packageMappingSource("measured Stats", "", "measured")
	sources := map[string][]byte{
		"stats_windows.go": []byte("package fixture; import s \"" + sharedPackage + "\"; type Stats = s.DependencyStats"),
		"stats_linux.go":   []byte("package fixture; type Stats int"),
		"consumer.go":      []byte(consumer),
		"local.go":         []byte(mappingFixture),
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 1 || findings[0].Path != "local.go" || findings[0].Advisory {
		t.Fatalf("ambiguous sibling supplied provenance or local mapping was lost: %+v err=%v", findings, err)
	}
	delete(sources, "stats_windows.go")
	delete(sources, "stats_linux.go")
	sources["types.go"] = []byte("package fixture; type Holder struct{}")
	sources["method_windows.go"] = []byte("package fixture; import s \"" + sharedPackage + "\"; func (Holder) stats() s.DependencyStats { panic(0) }")
	sources["method_linux.go"] = []byte("package fixture; func (*Holder) stats() int { return 0 }")
	sources["consumer.go"] = []byte(packageMappingSource("holder Holder", "measured := holder.stats()", "measured"))
	findings, err = AnalyzeSources(sources)
	if err != nil || len(findings) != 1 || findings[0].Path != "local.go" {
		t.Fatalf("ambiguous method supplied provenance: %+v err=%v", findings, err)
	}
}
