package jvm

import (
	"strings"
	"testing"
)

func TestPomPropertyExpansionRejectsOversizedValues(t *testing.T) {
	const limit = 64 * 1024
	for _, tc := range []struct {
		name, input string
		properties  map[string]string
	}{
		{name: "literal", input: strings.Repeat("x", limit+1)},
		{name: "replacement", input: "${large}", properties: map[string]string{"large": strings.Repeat("x", limit+1)}},
		{name: "multiplication", input: strings.Repeat("${chunk}", 128), properties: map[string]string{"chunk": strings.Repeat("x", 513)}},
		{name: "token work", input: strings.Repeat("${missing}", 1025)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, unresolved := resolvePomPropertyValue(tc.input, tc.properties)
			if value != "" || !unresolved {
				t.Fatalf("expected rejected expansion, got %d bytes, unresolved=%v", len(value), unresolved)
			}
		})
	}
}

func TestPomPropertyExpansionPreservesBoundedValues(t *testing.T) {
	const limit = 64 * 1024
	for _, tc := range []struct {
		name, input, want string
		properties        map[string]string
		unresolved        bool
	}{
		{name: "exact limit", input: "${value}", properties: map[string]string{"value": strings.Repeat("x", limit)}, want: strings.Repeat("x", limit)},
		{name: "nested", input: "${group}:${artifact}", properties: map[string]string{"group": "${prefix}.example", "prefix": "org", "artifact": "library"}, want: "org.example:library"},
		{name: "missing", input: "${missing}", want: "${missing}", unresolved: true},
		{name: "cycle", input: "${cycle}", properties: map[string]string{"cycle": "${cycle}"}, want: "${cycle}", unresolved: true},
		{name: "empty", input: "  "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, unresolved := resolvePomPropertyValue(tc.input, tc.properties)
			if value != tc.want || unresolved != tc.unresolved {
				t.Fatalf("unexpected bounded expansion: %d bytes, unresolved=%v", len(value), unresolved)
			}
		})
	}
}

func TestPomPropertyExpansionStopsRecursiveAmplification(t *testing.T) {
	value, unresolved := resolvePomPropertyValue("${x}", map[string]string{"x": "${x}${x}"})
	if value != "" || !unresolved {
		t.Fatalf("expected rejected recursive expansion, got %d bytes, unresolved=%v", len(value), unresolved)
	}
}

func TestPomPropertyExpansionPreservesUnresolvedDependencyHandling(t *testing.T) {
	content := `<project><properties><large>` + strings.Repeat("x", 64*1024+1) + `</large></properties>
<dependencies><dependency><groupId>${large}</groupId><artifactId>rejected</artifactId></dependency></dependencies>
<dependencyManagement><dependencies><dependency><groupId>example</groupId><artifactId>kept</artifactId><version>${large}</version></dependency></dependencies></dependencyManagement></project>`
	descriptors, warnings := parsePomDependencyContent("pom.xml", content)
	if len(descriptors) != 1 || descriptors[0].Artifact != "kept" {
		t.Fatalf("unexpected bounded dependency result: %#v", descriptors)
	}
	const warning = "unable to resolve managed Maven version for example:kept in pom.xml"
	if len(warnings) != 1 || warnings[0] != warning {
		t.Fatalf("expected unresolved version warning, got %#v", warnings)
	}
}
