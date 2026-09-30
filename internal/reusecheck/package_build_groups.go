package reusecheck

import (
	"go/ast"
	"go/token"
)

func sourceAnalysisGroups(files []*ast.File, sources map[string][]byte, positions *token.FileSet) []analysisGroup {
	predicates := make(map[*ast.File]*sourceBuildPredicate, len(files))
	unconditional := true
	for _, file := range files {
		path := positions.PositionFor(file.Pos(), false).Filename
		predicate := sourceBuildConstraint(path, sources[path])
		predicates[file] = predicate
		unconditional = unconditional && predicate.unconditional()
	}
	if unconditional {
		return analysisGroups(files)
	}
	return constrainedAnalysisGroups(files, predicates)
}

// A target receives only declarations guaranteed to exist whenever it is built.
// Identical contexts share parsing and binding work; every target emits once.
func constrainedAnalysisGroups(files []*ast.File, predicates map[*ast.File]*sourceBuildPredicate) []analysisGroup {
	var groups []analysisGroup
	contexts := make(map[string]int)
	for _, target := range files {
		group, identity := constrainedAnalysisGroup(target, files, predicates)
		if index, found := contexts[identity]; found {
			groups[index].targets[target] = true
			continue
		}
		contexts[identity] = len(groups)
		groups = append(groups, group)
	}
	return groups
}

func constrainedAnalysisGroup(target *ast.File, files []*ast.File, predicates map[*ast.File]*sourceBuildPredicate) (analysisGroup, string) {
	possible := possibleAnalysisFiles(target, files, predicates)
	keys, owners := analysisBuildDeclarations(files, possible)
	group := analysisGroup{targets: map[*ast.File]bool{target: true}}
	identity := make([]byte, len(files))
	for index, file := range files {
		identity[index] = 'x'
		if !possible[file] {
			continue
		}
		if file == target || predicates[target].implies(predicates[file]) && unambiguousAnalysisKeys(file, keys[file], owners) {
			group.files = append(group.files, file)
			identity[index] = 'i'
		} else {
			group.omitted = append(group.omitted, file)
			identity[index] = 'o'
		}
	}
	return group, string(identity)
}

func possibleAnalysisFiles(target *ast.File, files []*ast.File, predicates map[*ast.File]*sourceBuildPredicate) map[*ast.File]bool {
	possible := make(map[*ast.File]bool, len(files))
	for _, file := range files {
		if file == target || !predicates[target].excludes(predicates[file]) {
			possible[file] = true
		}
	}
	return possible
}

func analysisBuildDeclarations(files []*ast.File, possible map[*ast.File]bool) (map[*ast.File][]string, map[string][]*ast.File) {
	selected := make([]*ast.File, 0, len(possible))
	for _, file := range files {
		if possible[file] {
			selected = append(selected, file)
		}
	}
	types := analysisTypeDeclarations(selected)
	keys := make(map[*ast.File][]string, len(selected))
	owners := make(map[string][]*ast.File)
	for _, file := range selected {
		keys[file] = analysisDeclarationKeysWithTypes(file, types)
		for _, key := range keys[file] {
			owners[key] = append(owners[key], file)
		}
	}
	return keys, owners
}

func unambiguousAnalysisKeys(file *ast.File, keys []string, owners map[string][]*ast.File) bool {
	for _, key := range keys {
		for _, owner := range owners[key] {
			if owner != file {
				return false
			}
		}
	}
	return true
}
