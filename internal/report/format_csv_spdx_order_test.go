package report

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCSVSPDXDuplicateDependenciesAreOrderIndependent(t *testing.T) {
	cases := []struct {
		name         string
		dependencies []DependencyReport
	}{
		{name: "versions", dependencies: []DependencyReport{
			csvSPDXDuplicate("1.0.0", "MIT"), csvSPDXDuplicate("2.0.0", "MIT"), csvSPDXDuplicate("3.0.0", "MIT"),
		}},
		{name: "same identity different visible data", dependencies: []DependencyReport{
			csvSPDXDuplicate("1.0.0", "MIT"), csvSPDXDuplicate("1.0.0", "Apache-2.0"), csvSPDXDuplicate("1.0.0", "BSD-3-Clause"),
		}},
		{name: "missing identity", dependencies: []DependencyReport{
			{Language: "js-ts", Name: "duplicate", UsedExportsCount: 1, License: &DependencyLicense{SPDX: "MIT"}},
			{Language: "js-ts", Name: "duplicate", UsedExportsCount: 2, License: &DependencyLicense{SPDX: "Apache-2.0"}},
			{Language: "js-ts", Name: "duplicate", UsedExportsCount: 3, License: &DependencyLicense{SPDX: "BSD-3-Clause"}},
		}},
		{name: "mixed nil and empty identity", dependencies: []DependencyReport{
			{Language: "js-ts", Name: "duplicate"},
			{Language: "js-ts", Name: "duplicate", Identity: &DependencyIdentity{}},
			{Language: "js-ts", Name: "duplicate"},
		}},
		{name: "identical rows", dependencies: []DependencyReport{
			csvSPDXDuplicate("1.0.0", "MIT"), csvSPDXDuplicate("1.0.0", "MIT"), csvSPDXDuplicate("1.0.0", "MIT"),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reportData := Report{SchemaVersion: SchemaVersion, RepoPath: "/repo", Dependencies: tc.dependencies}
			wantCSV, wantSPDX := csvSPDXOutputs(t, reportData)
			wantDocument := csvSPDXDocument(t, wantSPDX)
			for _, order := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
				reportData.Dependencies = []DependencyReport{tc.dependencies[order[0]], tc.dependencies[order[1]], tc.dependencies[order[2]]}
				assertCSVSPDXDuplicatePermutation(t, reportData, order, wantCSV, wantSPDX, wantDocument)
			}
		})
	}
}

func assertCSVSPDXDuplicatePermutation(t *testing.T, reportData Report, order [3]int, wantCSV, wantSPDX string, wantDocument spdxDocument) {
	t.Helper()
	original := append([]DependencyReport(nil), reportData.Dependencies...)
	originalValues := csvSPDXInputSnapshot(t, reportData.Dependencies)
	gotCSV, gotSPDX := csvSPDXOutputs(t, reportData)
	if gotCSV != wantCSV {
		t.Errorf("rendered CSV output depends on input order %v", order)
	}
	if gotSPDX != wantSPDX {
		t.Errorf("rendered SPDX output depends on input order %v", order)
	}
	if !reflect.DeepEqual(reportData.Dependencies, original) {
		t.Fatal("formatting mutated input dependency order")
	}
	if csvSPDXInputSnapshot(t, reportData.Dependencies) != originalValues {
		t.Fatal("formatting mutated input dependency values")
	}
	gotDocument := csvSPDXDocument(t, gotSPDX)
	if gotDocument.DocumentNamespace != wantDocument.DocumentNamespace {
		t.Errorf("rendered SPDX namespace depends on input order %v", order)
	}
	if !reflect.DeepEqual(gotDocument.Packages, wantDocument.Packages) {
		t.Errorf("rendered SPDX IDs changed their package attachment for order %v", order)
	}
}

func csvSPDXInputSnapshot(t *testing.T, dependencies []DependencyReport) string {
	t.Helper()
	payload, err := json.Marshal(dependencies)
	if err != nil {
		t.Fatalf("snapshot input dependencies: %v", err)
	}
	return string(payload)
}

func csvSPDXDuplicate(version, license string) DependencyReport {
	return DependencyReport{
		Language: "js-ts", Name: "duplicate",
		Identity: &DependencyIdentity{Ecosystem: "npm", Name: "duplicate", Version: version, PURL: "pkg:npm/duplicate@" + version},
		License:  &DependencyLicense{SPDX: license},
	}
}

func csvSPDXOutputs(t *testing.T, reportData Report) (string, string) {
	t.Helper()
	csvOutput, err := formatCSV(reportData)
	if err != nil {
		t.Fatalf("format CSV: %v", err)
	}
	spdxOutput, err := formatSPDXJSON(reportData)
	if err != nil {
		t.Fatalf("format SPDX: %v", err)
	}
	return csvOutput, spdxOutput
}

func csvSPDXDocument(t *testing.T, output string) spdxDocument {
	t.Helper()
	var doc spdxDocument
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		t.Fatalf("decode SPDX: %v", err)
	}
	if len(doc.Packages) != 3 || len(doc.Relationships) != 3 {
		t.Fatalf("unexpected package/relation counts: %d/%d", len(doc.Packages), len(doc.Relationships))
	}
	ids := make(map[string]bool)
	for i, pkg := range doc.Packages {
		if ids[pkg.SPDXID] {
			t.Fatalf("duplicate package ID: %s", pkg.SPDXID)
		}
		ids[pkg.SPDXID] = true
		relation := doc.Relationships[i]
		if relation.SPDXElementID != spdxDocumentRef || relation.RelationshipType != "DESCRIBES" || relation.RelatedSPDXElement != pkg.SPDXID {
			t.Fatalf("relationship does not describe its package: %#v", relation)
		}
	}
	return doc
}

func TestCSVSPDXDependencyOrderUsesNormalizedIdentity(t *testing.T) {
	cases := []struct {
		name          string
		first, second DependencyIdentity
	}{
		{name: "ecosystem", first: DependencyIdentity{Ecosystem: " ruby ", Name: "z"}, second: DependencyIdentity{Ecosystem: "npm", Name: "a"}},
		{name: "namespace", first: DependencyIdentity{Ecosystem: "npm", Namespace: " a ", Name: "z"}, second: DependencyIdentity{Ecosystem: "npm", Namespace: "b", Name: "a"}},
		{name: "name", first: DependencyIdentity{Ecosystem: "pypi", Name: "A_package"}, second: DependencyIdentity{Ecosystem: "pypi", Name: "b-package"}},
		{name: "version", first: DependencyIdentity{Version: " 1.0.0 "}, second: DependencyIdentity{Version: "2.0.0"}},
		{name: "purl", first: DependencyIdentity{PURL: "PKG:NPM/a@1.0.0"}, second: DependencyIdentity{PURL: "pkg:npm/b@1.0.0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := DependencyReport{Language: "js-ts", Name: "duplicate", Identity: &tc.first, UsedExportsCount: 2}
			second := DependencyReport{Language: "js-ts", Name: "duplicate", Identity: &tc.second, UsedExportsCount: 1}
			for _, input := range [][]DependencyReport{{second, first}, {first, second}} {
				got := sortedDependenciesForCSV(input)
				if !reflect.DeepEqual(got, []DependencyReport{first, second}) {
					t.Errorf("dependencies did not follow normalized %s order", tc.name)
				}
			}
		})
	}
}
