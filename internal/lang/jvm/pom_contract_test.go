package jvm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPomInventoryCharacterization(t *testing.T) {
	data, err := os.ReadFile("../shared/testdata/pom-consumers.xml")
	if err != nil {
		t.Fatal(err)
	}
	descriptors, warnings := parsePomDependencyContent("pom.xml", string(data))
	actual, err := json.Marshal(struct {
		Dependencies []dependencyDescriptor
		Warnings     []string
	}{descriptors, warnings})
	if err != nil {
		t.Fatal(err)
	}
	assertPomInventoryGolden(t, actual)
}

func assertPomInventoryGolden(t *testing.T, actual []byte) {
	t.Helper()
	path := filepath.Join("../shared/testdata", "pom-inventory.json")

	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(expected) != string(actual) {
		t.Fatalf("consumer output changed:\n%s\nwant:\n%s", actual, expected)
	}
	t.Logf("normalized-consumer-output: %s", actual)
}

func TestPomInventoryMalformedCharacterization(t *testing.T) {
	descriptors, warnings := parsePomDependencyContent("pom.xml", "<project>")
	if !reflect.DeepEqual(descriptors, []dependencyDescriptor(nil)) || !reflect.DeepEqual(warnings, []string{"unable to parse Maven POM pom.xml: XML syntax error on line 1: unexpected EOF"}) {
		t.Fatalf("unexpected diagnostics/collections: %#v %#v", descriptors, warnings)
	}
	descriptors, warnings = parsePomDependencyContent("pom.xml", "<project/>")
	if !reflect.DeepEqual(descriptors, []dependencyDescriptor{}) || !reflect.DeepEqual(warnings, []string(nil)) {
		t.Fatalf("empty semantics changed: %#v %#v", descriptors, warnings)
	}
}

func TestPomInventoryPreservesEmptyTokenBesideResolvedProperty(t *testing.T) {
	content := `<project><properties><name>resolved</name></properties><dependencies><dependency><groupId>example</groupId><artifactId>${}literal-${name}</artifactId></dependency></dependencies></project>`
	descriptors, warnings := parsePomDependencyContent("pom.xml", content)
	want := []dependencyDescriptor{{Name: "${}literal-resolved", Group: "example", Artifact: "${}literal-resolved"}}
	if !reflect.DeepEqual(descriptors, want) || len(warnings) != 0 {
		t.Fatalf("literal empty token changed inventory: %#v, warnings %#v", descriptors, warnings)
	}
}

func TestPomInventoryRejectsOverflowingExpansionPrefix(t *testing.T) {
	const limit = 64 * 1024
	for _, tc := range []struct {
		name    string
		padding int
		keep    bool
	}{
		{name: "exact limit", padding: limit - 3, keep: true},
		{name: "overflowing prefix", padding: limit - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			large := strings.Repeat("x", tc.padding)
			content := `<project><properties><large>` + large + `</large><end>z</end></properties><dependencies><dependency><groupId>example</groupId><artifactId>${large}XX${end}</artifactId></dependency><dependency><groupId>example</groupId><artifactId>kept</artifactId></dependency></dependencies></project>`
			descriptors, warnings := parsePomDependencyContent("pom.xml", content)
			want := []dependencyDescriptor{{Name: "kept", Group: "example", Artifact: "kept"}}
			if tc.keep {
				artifact := large + "XXz"
				want = append(want, dependencyDescriptor{Name: artifact, Group: "example", Artifact: artifact})
			}
			if !reflect.DeepEqual(descriptors, want) || len(warnings) != 0 {
				t.Fatalf("expansion boundary changed inventory: %d descriptors, want %d; warnings %#v", len(descriptors), len(want), warnings)
			}
		})
	}
}
