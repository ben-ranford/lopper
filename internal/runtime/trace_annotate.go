package runtime

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

const (
	externalRuntimeContextPrefix = "external:"
	literalRuntimeContextPrefix  = "literal:"
)

func Annotate(rep report.Report, trace Trace, opts AnnotateOptions) report.Report {
	supported := supportedRuntimeLanguages(opts.SupportedLanguages)
	seen := annotateExistingDependencies(&rep, trace, supported)
	if opts.IncludeRuntimeOnlyRows {
		appendRuntimeOnlyDependencies(&rep, trace, seen, supported)
		sortDependencies(rep.Dependencies)
	}
	return rep
}

func annotateExistingDependencies(rep *report.Report, trace Trace, supported map[string]struct{}) map[DependencyKey]struct{} {
	seen := make(map[DependencyKey]struct{}, len(rep.Dependencies))
	for i := range rep.Dependencies {
		dep := &rep.Dependencies[i]
		language := dependencyRuntimeLanguage(*dep)
		if _, ok := supported[language]; !ok {
			continue
		}
		key := DependencyKey{Language: language, Name: dep.Name}
		seen[key] = struct{}{}
		annotateDependency(dep, trace, key, rep.RepoPath)
	}
	return seen
}

func annotateDependency(dep *report.DependencyReport, trace Trace, key DependencyKey, repoPath string) {
	loads := runtimeLoadCount(trace, key)
	hasStatic := hasStaticEvidence(*dep)
	if loads == 0 && !hasStatic && dep.RuntimeUsage == nil {
		return
	}
	correlation := runtimeCorrelation(hasStatic, loads > 0)
	dep.RuntimeUsage = &report.RuntimeUsage{
		LoadCount:     loads,
		Correlation:   correlation,
		RuntimeOnly:   correlation == report.RuntimeCorrelationRuntimeOnly,
		Modules:       runtimeModules(runtimeModuleCounts(trace, key)),
		ParentModules: runtimeContextModules(runtimeParentCounts(trace, key), repoPath),
		Entrypoints:   runtimeContextModules(runtimeEntrypointCounts(trace, key), repoPath),
		TopSymbols:    runtimeSymbols(runtimeSymbolCounts(trace, key)),
	}
}

func appendRuntimeOnlyDependencies(rep *report.Report, trace Trace, seen map[DependencyKey]struct{}, supported map[string]struct{}) {
	for _, key := range runtimeDependencyKeys(trace, supported) {
		loads := runtimeLoadCount(trace, key)
		if loads == 0 {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		rep.Dependencies = append(rep.Dependencies, report.DependencyReport{
			Language: key.Language,
			Name:     key.Name,
			RuntimeUsage: &report.RuntimeUsage{
				LoadCount:     loads,
				Correlation:   report.RuntimeCorrelationRuntimeOnly,
				RuntimeOnly:   true,
				Modules:       runtimeModules(runtimeModuleCounts(trace, key)),
				ParentModules: runtimeContextModules(runtimeParentCounts(trace, key), rep.RepoPath),
				Entrypoints:   runtimeContextModules(runtimeEntrypointCounts(trace, key), rep.RepoPath),
				TopSymbols:    runtimeSymbols(runtimeSymbolCounts(trace, key)),
			},
		})
	}
}

func supportedRuntimeLanguages(languages []string) map[string]struct{} {
	supported := make(map[string]struct{}, len(languages)+1)
	if len(languages) == 0 {
		supported[runtimeLanguageJSTS] = struct{}{}
		return supported
	}
	for _, language := range languages {
		normalized := normalizeRuntimeLanguage(language)
		if normalized == "" {
			continue
		}
		supported[normalized] = struct{}{}
	}
	return supported
}

func dependencyRuntimeLanguage(dep report.DependencyReport) string {
	return normalizeRuntimeLanguage(dep.Language)
}

func runtimeDependencyKeys(trace Trace, supported map[string]struct{}) []DependencyKey {
	seen := make(map[DependencyKey]struct{})
	for key, loads := range trace.DependencyLoadsByLanguage {
		if loads == 0 {
			continue
		}
		if _, ok := supported[key.Language]; ok {
			seen[key] = struct{}{}
		}
	}
	if _, ok := supported[runtimeLanguageJSTS]; ok {
		for dependency, loads := range trace.DependencyLoads {
			if loads > 0 {
				seen[DependencyKey{Language: runtimeLanguageJSTS, Name: dependency}] = struct{}{}
			}
		}
	}

	keys := make([]DependencyKey, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Language == keys[j].Language {
			return keys[i].Name < keys[j].Name
		}
		return keys[i].Language < keys[j].Language
	})
	return keys
}

func runtimeLoadCount(trace Trace, key DependencyKey) int {
	if trace.DependencyLoadsByLanguage != nil {
		if loads, ok := trace.DependencyLoadsByLanguage[key]; ok {
			return loads
		}
	}
	if key.Language == runtimeLanguageJSTS {
		return trace.DependencyLoads[key.Name]
	}
	return 0
}

func runtimeModuleCounts(trace Trace, key DependencyKey) map[string]int {
	return runtimeCountsForKey(trace.DependencyModulesByLanguage, trace.DependencyModules, key)
}

func runtimeParentCounts(trace Trace, key DependencyKey) map[string]int {
	return runtimeCountsForKey(trace.DependencyParentsByLanguage, trace.DependencyParents, key)
}

func runtimeEntrypointCounts(trace Trace, key DependencyKey) map[string]int {
	return runtimeCountsForKey(trace.DependencyEntrypointsByLanguage, trace.DependencyEntrypoints, key)
}

func runtimeSymbolCounts(trace Trace, key DependencyKey) map[string]int {
	return runtimeCountsForKey(trace.DependencySymbolsByLanguage, trace.DependencySymbols, key)
}

func runtimeCountsForKey(scoped map[DependencyKey]map[string]int, legacy map[string]map[string]int, key DependencyKey) map[string]int {
	if scoped != nil {
		if values, ok := scoped[key]; ok {
			return values
		}
	}
	if key.Language == runtimeLanguageJSTS {
		return legacy[key.Name]
	}
	return nil
}

func sortDependencies(dependencies []report.DependencyReport) {
	sort.Slice(dependencies, func(i, j int) bool {
		left := dependencies[i]
		right := dependencies[j]
		if left.Language == right.Language {
			return left.Name < right.Name
		}
		return left.Language < right.Language
	})
}

func hasStaticEvidence(dep report.DependencyReport) bool {
	return len(dep.UsedImports)+len(dep.UnusedImports)+len(dep.SuppressedUnusedImports) > 0
}

func runtimeCorrelation(hasStatic, hasRuntime bool) report.RuntimeCorrelation {
	switch {
	case hasStatic && hasRuntime:
		return report.RuntimeCorrelationOverlap
	case hasRuntime:
		return report.RuntimeCorrelationRuntimeOnly
	default:
		return report.RuntimeCorrelationStaticOnly
	}
}

func runtimeModules(values map[string]int) []report.RuntimeModuleUsage {
	if len(values) == 0 {
		return nil
	}
	items := make([]report.RuntimeModuleUsage, 0, len(values))
	for module, count := range values {
		items = append(items, report.RuntimeModuleUsage{Module: module, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Module < items[j].Module
		}
		return items[i].Count > items[j].Count
	})
	return items
}

func runtimeContextModules(values map[string]int, repoPath string) []report.RuntimeModuleUsage {
	if len(values) == 0 {
		return nil
	}
	redacted := make(map[string]int, len(values))
	for module, count := range values {
		visible := visibleRuntimeContextModule(module, repoPath)
		if visible == "" {
			continue
		}
		redacted[visible] += count
	}
	return runtimeModules(redacted)
}

func visibleRuntimeContextModule(value, repoPath string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value, fileURL, fileURLAuthority := normalizeRuntimeContextValue(value)
	if value == "" {
		return ""
	}
	if fileURLAuthority {
		return safeRuntimeContextBase(value)
	}
	if !fileURL && strings.Contains(value, "://") && !runtimeContextWindowsDrivePath(value) {
		return visibleRuntimeContextLiteral(value)
	}
	if !fileURL && !runtimeContextLooksLikePath(value) {
		return visibleRuntimeContextLiteral(value)
	}
	uncPath := strings.HasPrefix(value, "//")
	if uncPath {
		return safeRuntimeContextBase(value)
	}
	value = path.Clean(value)
	if repoRelative, ok := runtimeContextRepoRelativePath(repoPath, value); ok {
		return visibleRuntimeContextLiteral(repoRelative)
	}
	if fileURL || path.IsAbs(value) || runtimeContextWindowsDrivePath(value) || runtimeContextEscapesRoot(value) {
		return safeRuntimeContextBase(value)
	}
	return visibleRuntimeContextLiteral(value)
}

func visibleRuntimeContextLiteral(value string) string {
	if strings.HasPrefix(value, externalRuntimeContextPrefix) ||
		strings.HasPrefix(value, literalRuntimeContextPrefix) {
		return literalRuntimeContextPrefix + value
	}
	return value
}

func runtimeContextRepoRelativePath(repoPath, value string) (string, bool) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return "", false
	}
	repoPath = filepath.Clean(repoPath)
	if !filepath.IsAbs(repoPath) {
		return "", false
	}

	candidate := filepath.FromSlash(value)
	if (path.IsAbs(value) || runtimeContextWindowsDrivePath(value)) && !filepath.IsAbs(candidate) {
		return "", false
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(repoPath, candidate)
	} else {
		candidate = filepath.Clean(candidate)
	}

	resolvedRepoPath, repoErr := filepath.EvalSymlinks(repoPath)
	resolvedCandidate, candidateErr := filepath.EvalSymlinks(candidate)
	if repoErr == nil && candidateErr == nil {
		return runtimeContextRelativeToRoot(resolvedRepoPath, resolvedCandidate)
	}
	if relative, ok := runtimeContextRelativeToRoot(repoPath, candidate); ok {
		return relative, true
	}
	if repoErr == nil {
		return runtimeContextRelativeToRoot(resolvedRepoPath, candidate)
	}
	return "", false
}

func runtimeContextRelativeToRoot(repoPath, candidate string) (string, bool) {
	relative, err := filepath.Rel(repoPath, candidate)
	if err != nil {
		return "", false
	}
	relative = filepath.Clean(relative)
	if relative == "." || filepath.IsAbs(relative) || runtimeContextEscapesRoot(filepath.ToSlash(relative)) {
		return "", false
	}
	return filepath.ToSlash(relative), true
}

func normalizeRuntimeContextValue(value string) (string, bool, bool) {
	value = strings.ReplaceAll(value, `\`, "/")
	fileURL := len(value) >= len(fileURLPrefix) && strings.EqualFold(value[:len(fileURLPrefix)], fileURLPrefix)
	fileURLAuthority := false
	if fileURL {
		value = value[len(fileURLPrefix):]
		fileURLAuthority = value != "" &&
			!strings.HasPrefix(value, "/") &&
			!runtimeContextWindowsDrivePath(value)
		switch {
		case strings.HasPrefix(strings.ToLower(value), "localhost/"):
			value = value[len("localhost"):]
		case value != "" && !strings.HasPrefix(value, "/") && !runtimeContextWindowsDrivePath(value):
			value = "//" + value
		}
	}
	if len(value) >= 3 && value[0] == '/' && runtimeContextWindowsDrivePath(value[1:]) {
		value = value[1:]
	}
	return value, fileURL, fileURLAuthority
}

func runtimeContextLooksLikePath(value string) bool {
	return strings.Contains(value, "/") || strings.HasPrefix(value, ".") || runtimeContextWindowsDrivePath(value)
}

func runtimeContextWindowsDrivePath(value string) bool {
	value = strings.TrimPrefix(value, "/")
	return len(value) >= 2 &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':'
}

func runtimeContextEscapesRoot(value string) bool {
	return value == ".." || strings.HasPrefix(value, "../")
}

func safeRuntimeContextBase(value string) string {
	value = strings.TrimRight(value, "/")
	if runtimeContextWindowsDrivePath(value) {
		value = strings.TrimPrefix(value, "/")
		value = value[2:]
	}
	base := path.Base(value)
	if base == "" || base == "." || base == ".." || base == "/" || runtimeContextWindowsDrivePath(base) {
		return ""
	}
	return externalRuntimeContextPrefix + base
}

func runtimeSymbols(values map[string]int) []report.RuntimeSymbolUsage {
	if len(values) == 0 {
		return nil
	}
	items := make([]report.RuntimeSymbolUsage, 0, len(values))
	for key, count := range values {
		parts := strings.SplitN(key, "\x00", 2)
		symbol := key
		module := ""
		if len(parts) == 2 {
			module = parts[0]
			symbol = parts[1]
		}
		items = append(items, report.RuntimeSymbolUsage{Symbol: symbol, Module: module, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			if items[i].Symbol == items[j].Symbol {
				return items[i].Module < items[j].Module
			}
			return items[i].Symbol < items[j].Symbol
		}
		return items[i].Count > items[j].Count
	})
	if len(items) > 5 {
		items = items[:5]
	}
	return items
}
