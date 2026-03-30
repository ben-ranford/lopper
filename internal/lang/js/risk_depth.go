package js

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

func buildTransitiveDepthRiskCue(repoPath string, depRoot string, pkg packageJSON) *report.RiskCue {
	depth := estimateTransitiveDepth(repoPath, depRoot, pkg)
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

func estimateTransitiveDepth(repoPath string, depRoot string, pkg packageJSON) int {
	memo := make(map[string]int)
	visiting := make(map[string]struct{})
	return transitiveDepth(repoPath, depRoot, pkg, memo, visiting, 512)
}

func transitiveDepth(repoPath string, pkgRoot string, pkg packageJSON, memo map[string]int, visiting map[string]struct{}, budget int) int {
	if cached, ok := memo[pkgRoot]; ok {
		return cached
	}
	if budget <= 0 {
		return 1
	}
	if _, ok := visiting[pkgRoot]; ok {
		return 1
	}
	visiting[pkgRoot] = struct{}{}
	defer delete(visiting, pkgRoot)

	deps := collectDependencyNames(pkg)
	if len(deps) == 0 {
		memo[pkgRoot] = 1
		return 1
	}

	maxChild := 0
	for _, depName := range deps {
		childRoot, ok := resolveInstalledDependencyRoot(repoPath, pkgRoot, depName)
		if !ok {
			continue
		}
		childPkg, childWarnings := loadDependencyPackageJSON(childRoot)
		if len(childWarnings) > 0 {
			continue
		}
		childDepth := transitiveDepth(repoPath, childRoot, childPkg, memo, visiting, budget-1)
		if childDepth > maxChild {
			maxChild = childDepth
		}
	}

	total := 1 + maxChild
	memo[pkgRoot] = total
	return total
}

func resolveInstalledDependencyRoot(repoPath, currentPackageRoot, dependency string) (string, bool) {
	if !isSafeDependencyName(dependency) {
		return "", false
	}

	candidates := []string{
		filepath.Join(currentPackageRoot, "node_modules", dependencyPath(dependency)),
		filepath.Join(repoPath, "node_modules", dependencyPath(dependency)),
	}
	for _, root := range candidates {
		info, err := os.Stat(filepath.Join(root, "package.json"))
		if err == nil && !info.IsDir() {
			return root, true
		}
	}
	return "", false
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
