package jvm

import (
	"fmt"
	"strings"
	"testing"
)

func TestPomPropertyExpansionRejectsOversizedHelperValues(t *testing.T) {
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

func TestPomPropertyExpansionCountsIntroducedTokens(t *testing.T) {
	const tokenBudget = 1024
	value, unresolved := resolvePomPropertyValue("${first}${second}", map[string]string{
		"first":  strings.Repeat("${second}", tokenBudget),
		"second": "resolved",
	})
	if value != "" || !unresolved {
		t.Fatalf("expected over-budget introduced tokens to be rejected, got %d bytes, unresolved=%v", len(value), unresolved)
	}
}

func TestPomPropertyExpansionBuildsEachPassInOneScan(t *testing.T) {
	const tokenCount = 1023
	properties := make(map[string]string, tokenCount+1)
	var root strings.Builder
	var want strings.Builder
	for index := 0; index < tokenCount; index++ {
		key := fmt.Sprintf("p%d", index)
		root.WriteString("${")
		root.WriteString(key)
		root.WriteByte('}')
		properties[key] = strings.Repeat("x", 50)
		want.WriteString(properties[key])
	}
	properties["root"] = root.String()

	allocations := testing.AllocsPerRun(5, func() {
		got, unresolved := resolvePomPropertyValue("${root}", properties)
		if unresolved || got != want.String() {
			t.Fatal("bounded one-scan expansion returned an unexpected result")
		}
	})
	if allocations > 128 {
		t.Fatalf("expansion allocated %.0f objects for %d replacements; want one bounded scan", allocations, tokenCount+1)
	}
}

func TestPomPropertyExpansionStopsRecursiveAmplification(t *testing.T) {
	const maxValueBytes = 64 * 1024
	value, unresolved := resolvePomPropertyValue("${x}", map[string]string{"x": "${x}${x}"})
	if !unresolved || len(value) > maxValueBytes {
		t.Fatalf("expected bounded unresolved recursive expansion, got %d bytes, unresolved=%v", len(value), unresolved)
	}
}

func TestPomPropertyExpansionPreservesOrderedPass(t *testing.T) {
	properties := map[string]string{"j": "x"}
	var input strings.Builder
	for key := byte('a'); key <= 'j'; key++ {
		input.WriteString("${" + string(key) + "}")
		if key < 'j' {
			properties[string(key)] = "${" + string(key+1) + "}"
		}
	}
	got, replaced, missing, used := replacePomPropertyTokens(input.String(), properties, 1024)
	if got != strings.Repeat("x", 10) || !replaced || missing || used != 55 {
		t.Fatalf("ordered pass: got %q, replaced=%v, missing=%v, used=%d", got, replaced, missing, used)
	}
}

func TestPomPropertyExpansionSharesPOMBudget(t *testing.T) {
	var content strings.Builder
	content.WriteString("<project><properties><large>" + strings.Repeat("x", 64*1024) + "</large></properties><dependencies>")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&content, "<dependency><groupId>${large}</groupId><artifactId>a%d</artifactId></dependency>", i)
	}
	content.WriteString("</dependencies><dependencyManagement><dependencies><dependency><groupId>${large}</groupId><artifactId>managed</artifactId></dependency></dependencies></dependencyManagement></project>")
	descriptors, _ := parsePomDependencyContent("pom.xml", content.String())
	bytes := 0
	for _, descriptor := range descriptors {
		bytes += len(descriptor.Group) + len(descriptor.Artifact)
		if descriptor.Artifact == "managed" {
			t.Fatal("managed list reset the POM budget")
		}
	}
	if len(descriptors) == 0 || len(descriptors) >= 300 || bytes > maxPomExpansionBytes {
		t.Fatalf("unbounded aggregate result: %d descriptors, %d bytes", len(descriptors), bytes)
	}
}
