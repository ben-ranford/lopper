package js

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

const (
	riskCodeDynamicLoader = "dynamic-loader"
	riskCodeNativeModule  = "native-module"
	riskCodeDeepGraph     = "deep-transitive-graph"
)

func assessRiskCues(repoPath string, dependency string, dependencyRootPath string, surface ExportSurface) ([]report.RiskCue, []string) {
	depRoot := dependencyRootPath
	if depRoot == "" {
		root, err := dependencyRoot(repoPath, dependency)
		if err != nil {
			return nil, []string{fmt.Sprintf("unable to assess risk cues for %q: %v", dependency, err)}
		}
		depRoot = root
	}

	pkg, warnings := loadDependencyPackageJSON(depRoot)
	cues := make([]report.RiskCue, 0, 3)
	cues, warnings = appendDynamicRiskCue(cues, warnings, dependency, surface.EntryPoints)
	cues, warnings = appendNativeRiskCue(cues, warnings, dependency, depRoot, pkg)
	cues, warnings = appendDepthRiskCue(cues, warnings, dependency, repoPath, depRoot, pkg)

	sort.Slice(cues, func(i, j int) bool {
		return cues[i].Code < cues[j].Code
	})
	return cues, warnings
}

func appendDynamicRiskCue(cues []report.RiskCue, warnings []string, dependency string, entrypoints []string) ([]report.RiskCue, []string) {
	dynamicCount, samples, err := detectDynamicLoaderUsage(entrypoints)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("dynamic loader scan failed for %q: %v", dependency, err))
		return cues, warnings
	}
	if dynamicCount == 0 {
		return cues, warnings
	}
	msg := fmt.Sprintf("dynamic require/import usage found in %d dependency entrypoint location(s)", dynamicCount)
	if len(samples) > 0 {
		msg = fmt.Sprintf("%s (%s)", msg, strings.Join(samples, ", "))
	}
	cues = append(cues, report.RiskCue{
		Code:     riskCodeDynamicLoader,
		Severity: "medium",
		Message:  msg,
	})
	return cues, warnings
}

func appendNativeRiskCue(cues []report.RiskCue, warnings []string, dependency string, depRoot string, pkg packageJSON) ([]report.RiskCue, []string) {
	isNative, details, err := detectNativeModuleIndicators(depRoot, pkg)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("native module scan failed for %q: %v", dependency, err))
		return cues, warnings
	}
	if !isNative {
		return cues, warnings
	}
	msg := "dependency appears to include native module indicators"
	if len(details) > 0 {
		msg = fmt.Sprintf("%s (%s)", msg, strings.Join(details, ", "))
	}
	cues = append(cues, report.RiskCue{
		Code:     riskCodeNativeModule,
		Severity: "high",
		Message:  msg,
	})
	return cues, warnings
}

func appendDepthRiskCue(cues []report.RiskCue, warnings []string, dependency string, repoPath string, depRoot string, pkg packageJSON) ([]report.RiskCue, []string) {
	depth, err := estimateTransitiveDepth(repoPath, depRoot, pkg)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("transitive depth check failed for %q: %v", dependency, err))
		return cues, warnings
	}
	if depth < 4 {
		return cues, warnings
	}
	severity := "medium"
	if depth >= 7 {
		severity = "high"
	}
	cues = append(cues, report.RiskCue{
		Code:     riskCodeDeepGraph,
		Severity: severity,
		Message:  fmt.Sprintf("transitive dependency depth is %d levels", depth),
	})
	return cues, warnings
}

func loadDependencyPackageJSON(depRoot string) (packageJSON, []string) {
	pkgPath := filepath.Join(depRoot, "package.json")
	// #nosec G304 -- depRoot is resolved under repoPath/node_modules for the selected dependency.
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return packageJSON{}, []string{fmt.Sprintf("unable to read dependency metadata: %s", pkgPath)}
	}

	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return packageJSON{}, []string{fmt.Sprintf("failed to parse dependency metadata: %s", pkgPath)}
	}
	return pkg, nil
}

func detectDynamicLoaderUsage(entrypoints []string) (int, []string, error) {
	count := 0
	samples := make([]string, 0, 3)

	for _, entry := range entrypoints {
		if !isLikelyCodeAsset(entry) {
			continue
		}
		// #nosec G304 -- entrypoints are resolved from dependency exports under depRoot.
		content, err := os.ReadFile(entry)
		if err != nil {
			return 0, nil, err
		}
		lines := strings.Split(string(content), "\n")
		for idx, line := range lines {
			if hasDynamicCall(line, "require(") || hasDynamicCall(line, "import(") {
				count++
				if len(samples) < 3 {
					samples = append(samples, fmt.Sprintf("%s:%d", filepath.Base(entry), idx+1))
				}
			}
		}
	}

	return count, samples, nil
}

func hasDynamicCall(line string, token string) bool {
	search := line
	for {
		pos := strings.Index(search, token)
		if pos < 0 {
			return false
		}
		if isCommented(search[:pos]) {
			return false
		}
		if pos > 0 && isIdentifierByte(search[pos-1]) {
			search = search[pos+len(token):]
			continue
		}
		next := firstNonSpaceByte(search[pos+len(token):])
		if next != '\'' && next != '"' && next != '`' {
			return true
		}
		search = search[pos+len(token):]
	}
}

func isCommented(prefix string) bool {
	commentPos := strings.Index(prefix, "//")
	return commentPos >= 0
}

func isIdentifierByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '$'
}

func firstNonSpaceByte(value string) byte {
	for i := 0; i < len(value); i++ {
		if value[i] != ' ' && value[i] != '\t' && value[i] != '\r' {
			return value[i]
		}
	}
	return 0
}

func detectNativeModuleIndicators(depRoot string, pkg packageJSON) (bool, []string, error) {
	details := collectNativeMetadataIndicators(pkg)
	bindingDetails, err := detectBindingGyp(depRoot)
	if err != nil {
		return false, nil, err
	}
	details = append(details, bindingDetails...)
	nodeBinary, err := detectNodeBinary(depRoot)
	if err != nil {
		return false, nil, err
	}
	if nodeBinary != "" {
		details = append(details, nodeBinary)
	}
	return len(details) > 0, dedupeStrings(details), nil
}

func collectNativeMetadataIndicators(pkg packageJSON) []string {
	details := make([]string, 0, 3)
	if pkg.Gypfile {
		details = append(details, "package.json:gypfile")
	}
	for _, scriptName := range []string{"preinstall", "install", "postinstall"} {
		body := strings.ToLower(strings.TrimSpace(pkg.Scripts[scriptName]))
		if body == "" {
			continue
		}
		if strings.Contains(body, "node-gyp") || strings.Contains(body, "prebuild") || strings.Contains(body, "node-pre-gyp") || strings.Contains(body, "cmake-js") {
			details = append(details, fmt.Sprintf("scripts.%s", scriptName))
		}
	}
	return details
}

func detectBindingGyp(depRoot string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(depRoot, "binding.gyp")); err == nil {
		return []string{"binding.gyp"}, nil
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return nil, nil
}

func detectNodeBinary(depRoot string) (string, error) {
	const maxVisited = 600
	visited := 0
	found := ""
	walkErr := filepath.WalkDir(depRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		visited++
		if visited > maxVisited {
			return fs.SkipAll
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".node") {
			found = filepath.Base(path)
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && walkErr != fs.SkipAll {
		return "", walkErr
	}
	return found, nil
}

func estimateTransitiveDepth(repoPath string, depRoot string, pkg packageJSON) (int, error) {
	memo := make(map[string]int)
	visiting := make(map[string]struct{})
	return transitiveDepth(repoPath, depRoot, pkg, memo, visiting, 512)
}

func transitiveDepth(
	repoPath string,
	pkgRoot string,
	pkg packageJSON,
	memo map[string]int,
	visiting map[string]struct{},
	budget int,
) (int, error) {
	if cached, ok := memo[pkgRoot]; ok {
		return cached, nil
	}
	if budget <= 0 {
		return 1, nil
	}
	if _, ok := visiting[pkgRoot]; ok {
		return 1, nil
	}
	visiting[pkgRoot] = struct{}{}
	defer delete(visiting, pkgRoot)

	deps := collectDependencyNames(pkg)
	if len(deps) == 0 {
		memo[pkgRoot] = 1
		return 1, nil
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
		childDepth, err := transitiveDepth(repoPath, childRoot, childPkg, memo, visiting, budget-1)
		if err != nil {
			return 0, err
		}
		if childDepth > maxChild {
			maxChild = childDepth
		}
	}

	total := 1 + maxChild
	memo[pkgRoot] = total
	return total, nil
}

func resolveInstalledDependencyRoot(repoPath string, currentPackageRoot string, dependency string) (string, bool) {
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

func dedupeStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := set[value]; ok {
			continue
		}
		set[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
