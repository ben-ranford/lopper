package jvm

import (
	"strings"
	"testing"
)

func TestPomPropertyExpansionRejectsOversizedValues(t *testing.T) {
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
