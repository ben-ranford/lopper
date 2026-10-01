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

// Each scope contains only declarations guaranteed under its build condition.
// Equal binding contexts share parsing; findings later require target coverage.
func constrainedAnalysisGroups(files []*ast.File, predicates map[*ast.File]*sourceBuildPredicate) []analysisGroup {
	var groups []analysisGroup
	contexts := make(map[string]int)
	anchors := sourceBuildPlatformRegions()
	for _, file := range files {
		anchors = append(anchors, predicates[file])
	}
	for _, target := range files {
		goal := predicates[target]
		groups = appendBuildAnalysisGroup(groups, contexts, target, files, predicates, goal)
		for _, anchor := range anchors {
			condition := goal.and(anchor)
			if !condition.valid {
				continue
			}
			condition.prepare()
			if condition.witness {
				groups = appendBuildAnalysisGroup(groups, contexts, target, files, predicates, condition)
			}
		}
	}
	return groups
}

func appendBuildAnalysisGroup(groups []analysisGroup, contexts map[string]int, target *ast.File, files []*ast.File, predicates map[*ast.File]*sourceBuildPredicate, condition *sourceBuildPredicate) []analysisGroup {
	group, identity := constrainedAnalysisGroup(target, files, predicates, condition)
	index, found := contexts[identity]
	if !found {
		index = len(groups)
		contexts[identity] = index
		group.support = make(map[*ast.File]*analysisBuildSupport)
		groups = append(groups, group)
	}
	selected := &groups[index]
	selected.targets[target] = true
	if selected.support[target] == nil {
		selected.support[target] = &analysisBuildSupport{goal: predicates[target]}
	}
	support := selected.support[target]
	if condition == predicates[target] {
		support.direct = true
		support.contexts = nil
	} else if !support.direct {
		support.contexts = append(support.contexts, condition)
	}
	return groups
}

func constrainedAnalysisGroup(target *ast.File, files []*ast.File, predicates map[*ast.File]*sourceBuildPredicate, condition *sourceBuildPredicate) (analysisGroup, string) {
	possible := possibleAnalysisFiles(target, files, predicates, condition)
	keys, owners := analysisBuildDeclarations(files, possible)
	group := analysisGroup{targets: map[*ast.File]bool{target: true}}
	identity := make([]byte, len(files))
	for index, file := range files {
		identity[index] = 'x'
		if !possible[file] {
			continue
		}
		if file == target || condition.implies(predicates[file]) && unambiguousAnalysisKeys(file, keys[file], owners) {
			group.files = append(group.files, file)
			identity[index] = 'i'
		} else {
			group.omitted = append(group.omitted, file)
			identity[index] = 'o'
		}
	}
	return group, string(identity)
}

func possibleAnalysisFiles(target *ast.File, files []*ast.File, predicates map[*ast.File]*sourceBuildPredicate, condition *sourceBuildPredicate) map[*ast.File]bool {
	possible := make(map[*ast.File]bool, len(files))
	for _, file := range files {
		if file == target || !condition.excludes(predicates[file]) {
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
