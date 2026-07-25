package js

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

func buildTransitiveDepthRiskCueForDepth(depth int) *report.RiskCue {
	if depth < 4 {
		return nil
	}

	severity := "medium"
	if depth >= 7 {
		severity = "high"
	}

	return &report.RiskCue{
		Code:     riskCodeDeepGraph,
		Severity: severity,
		Message:  fmt.Sprintf("transitive dependency depth is %d levels", depth),
	}
}

type depthEvaluation struct {
	depth    int
	warnings []string
}

func estimateTransitiveDepth(repoPath string, depRoot string, pkg packageJSON) (int, []string) {
	visiting := make(map[string]struct{})
	result := transitiveDepth(repoPath, depRoot, pkg, map[string]depthEvaluation{}, visiting, 512)
	return result.depth, dedupeStrings(result.warnings)
}

func transitiveDepth(repoPath string, pkgRoot string, pkg packageJSON, memo map[string]depthEvaluation, visiting map[string]struct{}, budget int) depthEvaluation {
	if cached, ok := memo[pkgRoot]; ok {
		return cached
	}
	if budget <= 0 {
		return depthEvaluation{depth: 1}
	}
	if _, ok := visiting[pkgRoot]; ok {
		return depthEvaluation{depth: 1}
	}
	visiting[pkgRoot] = struct{}{}
	defer delete(visiting, pkgRoot)

	deps := collectDependencyNames(pkg)
	if len(deps) == 0 {
		result := depthEvaluation{depth: 1}
		memo[pkgRoot] = result
		return result
	}

	maxChild := 0
	warnings := make([]string, 0)
	for _, depName := range deps {
		childRoot, ok := resolveInstalledDependencyRoot(repoPath, pkgRoot, depName)
		if !ok {
			continue
		}
		childPkg, childWarnings := loadDependencyPackageJSONWithinBoundary(childRoot, repoPath)
		if len(childWarnings) > 0 {
			warnings = append(warnings, transitiveDepthWarnings(childWarnings)...)
			continue
		}
		childDepth := transitiveDepth(repoPath, childRoot, childPkg, memo, visiting, budget-1)
		warnings = append(warnings, childDepth.warnings...)
		if childDepth.depth > maxChild {
			maxChild = childDepth.depth
		}
	}

	result := depthEvaluation{
		depth:    1 + maxChild,
		warnings: dedupeStrings(warnings),
	}
	memo[pkgRoot] = result
	return result
}

func resolveInstalledDependencyRoot(repoPath, currentPackageRoot, dependency string) (string, bool) {
	if !isSafeDependencyName(dependency) {
		return "", false
	}
	root, status := resolveDependencyRootFromDirDetailed(repoPath, currentPackageRoot, dependency)
	if status == dependencyRootFound {
		return root, true
	}
	return "", false
}

func transitiveDepthWarnings(childWarnings []string) []string {
	warnings := make([]string, 0, len(childWarnings))
	for _, warning := range childWarnings {
		warnings = append(warnings, fmt.Sprintf("transitive dependency depth is incomplete: %s", warning))
	}
	return warnings
}

func loadDependencyPackageJSONWithinBoundary(depRoot, allowedRoot string) (pkg packageJSON, warnings []string) {
	root, validatedDepRoot, err := openValidatedRootNoFollowWithinBoundary(depRoot, allowedRoot)
	if err != nil {
		return packageJSON{}, []string{fmt.Sprintf("unable to read dependency metadata: %s", filepath.Join(depRoot, jsPackageFile))}
	}
	defer func() {
		if closeRootAppendWarning(root, &warnings, fmt.Sprintf("unable to read dependency metadata: %s", filepath.Join(validatedDepRoot, jsPackageFile))) {
			pkg = packageJSON{}
		}
	}()
	return loadDependencyPackageJSONFromRoot(root, validatedDepRoot)
}

func dependencyPath(dependency string) string {
	if strings.HasPrefix(dependency, "@") {
		parts := strings.SplitN(dependency, "/", 2)
		if len(parts) == 2 {
			return filepath.Join(parts[0], parts[1])
		}
	}
	return dependency
}

func isSafeDependencyName(dependency string) bool {
	if dependency == "" {
		return false
	}
	if strings.HasPrefix(dependency, "@") {
		parts := strings.Split(dependency, "/")
		if len(parts) != 2 {
			return false
		}
		return isSafeDependencySegment(strings.TrimPrefix(parts[0], "@")) && isSafeDependencySegment(parts[1])
	}
	return isSafeDependencySegment(dependency)
}

func isSafeDependencySegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	if strings.ContainsAny(segment, `/\`) {
		return false
	}
	return true
}

func collectDependencyNames(pkg packageJSON) []string {
	set := make(map[string]struct{})
	for dep := range pkg.Dependencies {
		set[dep] = struct{}{}
	}
	for dep := range pkg.OptionalDependencies {
		set[dep] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for dep := range set {
		out = append(out, dep)
	}
	sort.Strings(out)
	return out
}
