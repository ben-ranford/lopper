package dotnet

import "testing"

func TestProjectDependencyMapperAppliesFallbackOnlyToProjectOwners(t *testing.T) {
	validOwnerMeta := &mappingMetadata{undeclaredByDependency: make(map[string]int)}
	dependency, resolved := resolveImportDependency("Bar", newProjectDependencyMapper([]string{"Foo"}, true), validOwnerMeta)
	if !resolved || dependency != "bar" || validOwnerMeta.undeclaredByDependency["bar"] != 1 {
		t.Fatalf("expected a valid project owner to retain an undeclared Bar finding, got dependency=%q resolved=%t metadata=%#v", dependency, resolved, validOwnerMeta)
	}

	unownedMeta := &mappingMetadata{undeclaredByDependency: make(map[string]int)}
	dependency, resolved = resolveImportDependency("Bar", newProjectDependencyMapper(nil, false), unownedMeta)
	if resolved || dependency != "" || len(unownedMeta.undeclaredByDependency) != 0 {
		t.Fatalf("expected an unowned malformed source to suppress fallback mapping, got dependency=%q resolved=%t metadata=%#v", dependency, resolved, unownedMeta)
	}
}
