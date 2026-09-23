package php

import (
	"context"
	"path/filepath"
	"testing"
)

func TestScanRepoIgnoresDynamicLoadingInCommentsAndStrings(t *testing.T) {
	for _, fragment := range []string{
		"// class_exists($name);", "# interface_exists($name);", "/* trait_exists($name); */",
		`$doc = 'method_exists(';`, `$doc = "new $type";`, "$doc = <<<'DOC'\nclass_exists($name);\nDOC;", "$doc = <<<DOC\ninterface_exists($name);\nDOC;",
	} {
		t.Run(fragment, func(t *testing.T) {
			scan := scanDynamicLoadingFixture(t, fragment)
			if scan.DynamicUsageByDependency[helpersVendorLibDependency] != 0 {
				t.Fatalf("non-code text marked dependency as dynamically used: %#v", scan.DynamicUsageByDependency)
			}
			if containsWarning(scan.Warnings, "dynamic loading/reflection patterns") {
				t.Fatalf("non-code text produced dynamic-loading warning: %v", scan.Warnings)
			}
		})
	}
}

func TestScanRepoPreservesExecutableDynamicLoading(t *testing.T) {
	for _, fragment := range []string{"class_exists('Vendor\\\\Lib\\\\Client');", "interface_exists($name);", "trait_exists($name);", "method_exists($name, 'run');", "new $type;", "$type::run();"} {
		t.Run(fragment, func(t *testing.T) {
			scan := scanDynamicLoadingFixture(t, fragment)
			if scan.DynamicUsageByDependency[helpersVendorLibDependency] != 1 {
				t.Fatalf("executable dynamic loading not retained: %#v", scan.DynamicUsageByDependency)
			}
		})
	}
}

func scanDynamicLoadingFixture(t *testing.T, fragment string) scanResult {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "source.php"), "<?php\nuse Vendor\\Lib\\Client;\n"+fragment+"\n")
	scan, err := scanRepo(context.Background(), repo, composerData{
		DeclaredDependencies: map[string]struct{}{helpersVendorLibDependency: {}},
		NamespaceToDep:       map[string]string{"Vendor\\Lib": helpersVendorLibDependency},
		LocalNamespaces:      map[string]struct{}{},
	})
	if err != nil {
		t.Fatalf("scan dynamic loading fixture: %v", err)
	}
	return scan
}
