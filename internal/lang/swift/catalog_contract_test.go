package swift

import (
	"reflect"
	"testing"
)

func TestEmptyCatalogWarnsForEnabledManagers(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "carthage"}[enabled], func(t *testing.T) {
			assertEmptyCatalogWarnings(t, enabled)
		})
	}
}

func assertEmptyCatalogWarnings(t *testing.T, enabled bool) {
	t.Helper()
	catalog, warnings, err := buildDependencyCatalogWithOptions(t.TempDir(), dependencyCatalogOptions{EnableCarthage: enabled})
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Dependencies) != 0 {
		t.Fatalf("missing manifests produced dependencies: %#v", catalog.Dependencies)
	}
	want := []string{"Package.swift not found; dependency declaration mapping may be incomplete", "Package.resolved not found; version/resolution mapping may be incomplete"}
	if enabled {
		want = append(want, "Cartfile not found; Carthage declaration mapping may be incomplete", "Cartfile.resolved not found; Carthage version/resolution mapping may be incomplete")
	}
	summary := "no Swift dependencies were discovered from Package.swift, Package.resolved, Podfile, Podfile.lock"
	if enabled {
		summary += ", Cartfile, Cartfile.resolved"
	}
	want = append(want, summary)
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("warnings = %v, want %v", warnings, want)
	}
}
