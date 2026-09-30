package reusecheck

import (
	"go/token"
	"strings"
	"testing"
)

func TestIndependentPackageVariantAxes(t *testing.T) {
	sources := map[string][]byte{
		"types_amd64.go":            []byte("package fixture; type Stats = Counts"),
		"types_arm64.go":            []byte("package fixture; type Stats struct{}"),
		"counts_linux.go":           []byte(`package fixture; import s "` + sharedPackage + `"; type Counts = s.DependencyStats`),
		"counts_windows.go":         []byte("package fixture; type Counts struct{}"),
		"consumer_linux_amd64.go":   []byte(packageMappingSource("measured Stats", "", "measured")),
		"consumer_windows_amd64.go": []byte("package fixture; func build() {}"),
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 1 || findings[0].Advisory || findings[0].Path != "consumer_linux_amd64.go" {
		t.Fatalf("compatible architecture provider was dropped: %+v error=%v", findings, err)
	}
}

func TestBuildContextsCoalesceTargets(t *testing.T) {
	sources := map[string][]byte{
		"common.go":             []byte("package fixture; type Common struct{}"),
		"first_linux_amd64.go":  []byte("package fixture; func first() {}"),
		"second_linux_amd64.go": []byte("package fixture; func second() {}"),
		"types_amd64.go":        []byte("package fixture; type Stats = int"),
		"types_arm64.go":        []byte("package fixture; type Stats = string"),
	}
	fset := token.NewFileSet()
	packages, err := parseSourcePackages(sources, fset)
	if err != nil {
		t.Fatal(err)
	}
	for _, files := range packages {
		groups := sourceAnalysisGroups(files, sources, fset)
		if len(groups) >= len(files) {
			t.Fatalf("equal target contexts did not coalesce: %d groups for %d files", len(groups), len(files))
		}
		seen := make(map[string]bool)
		for _, group := range groups {
			for target := range group.targets {
				path := fset.PositionFor(target.Pos(), false).Filename
				if seen[path] {
					t.Fatalf("target %s belongs to multiple output scopes", path)
				}
				seen[path] = true
			}
		}
		if len(seen) != len(files) {
			t.Fatalf("coalesced contexts lost targets: %v", seen)
		}
	}
}

func TestUniqueIncompatibleProviderRemainsUnknown(t *testing.T) {
	sources := map[string][]byte{
		"types_windows.go":  []byte(`package fixture; import s "` + sharedPackage + `"; type Stats = s.DependencyStats`),
		"consumer_linux.go": []byte(packageMappingSource("measured Stats", "", "measured")),
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 0 {
		t.Fatalf("unique but excluded declaration supplied provenance: %+v error=%v", findings, err)
	}
}

func TestBuildVariantShadowsRespectCoactivity(t *testing.T) {
	for _, other := range []struct {
		path, header string
		count        int
	}{
		{"other_windows.go", "", 1},
		{"other.go", "//go:build custom\n\n", 0},
	} {
		sources := map[string][]byte{
			"copy_linux.go": []byte(localCollectionSource("var copy = " + collectionFunctionLiteral("trimmed"))),
			other.path:      []byte(other.header + "package fixture; func len(any) int { return 0 }"),
		}
		findings, err := AnalyzeSources(sources)
		if err != nil || len(findings) != other.count {
			t.Fatalf("shadow %s coactivity changed ownership: %+v error=%v", other.path, findings, err)
		}
	}
}

func TestBuildVariantProviderKeepsImportOwnership(t *testing.T) {
	consumer := packageMappingSource("measured Stats", "", "measured")
	consumer = strings.Replace(consumer, "package fixture", "//go:build linux && amd64 && custom\n\npackage fixture", 1)
	sources := map[string][]byte{
		"owned.go":    []byte("//go:build amd64 && custom\n\npackage fixture; import . \"" + sharedPackage + "\"; type Stats = DependencyStats"),
		"other.go":    []byte("//go:build arm64 || !custom\n\npackage fixture; import . \"example.com/shared\"; type Stats = DependencyStats"),
		"consumer.go": []byte(consumer),
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 1 || findings[0].Advisory || findings[0].Path != "consumer.go" {
		t.Fatalf("tag proof lost the provider's dot import ownership: %+v error=%v", findings, err)
	}
}

func TestPlatformAliasesSelectOnlyGuaranteedProviders(t *testing.T) {
	for _, tc := range []struct {
		target, provider string
		count            int
	}{
		{"consumer_android.go", "types_linux.go", 1},
		{"consumer_ios.go", "types_darwin.go", 1},
		{"consumer_illumos.go", "types_solaris.go", 1},
		{"consumer_linux.go", "types_android.go", 0},
		{"linux.go", "types_linux.go", 0},
	} {
		sources := map[string][]byte{
			tc.target:   []byte(packageMappingSource("measured Stats", "", "measured")),
			tc.provider: []byte(`package fixture; import s "` + sharedPackage + `"; type Stats = s.DependencyStats`),
		}
		findings, err := AnalyzeSources(sources)
		if err != nil || len(findings) != tc.count {
			t.Fatalf("target %s and provider %s: %+v error=%v", tc.target, tc.provider, findings, err)
		}
	}
}

func TestBuildVariantProofUsesPhysicalFilename(t *testing.T) {
	sources := map[string][]byte{
		"consumer_linux_amd64.go": []byte("//line consumer_windows_arm64.go:1\n" + packageMappingSource("measured Stats", "", "measured")),
		"types_amd64.go":          []byte(`package fixture; import s "` + sharedPackage + `"; type Stats = s.DependencyStats`),
		"types_arm64.go":          []byte("package fixture; type Stats struct{}"),
	}
	findings, err := AnalyzeSources(sources)
	if err != nil || len(findings) != 1 || findings[0].Path != "consumer_linux_amd64.go" || findings[0].Advisory {
		t.Fatalf("line directive changed implicit build constraints: %+v error=%v", findings, err)
	}
}
