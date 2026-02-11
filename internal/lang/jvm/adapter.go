package jvm

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	Clock func() time.Time
}

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "jvm"
}

func (a *Adapter) Aliases() []string {
	return []string{"java", "kotlin"}
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	detection, err := a.DetectWithConfidence(ctx, repoPath)
	if err != nil {
		return false, err
	}
	return detection.Matched, nil
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	_ = ctx
	if repoPath == "" {
		repoPath = "."
	}

	detection := language.Detection{}
	roots := make(map[string]struct{})

	rootSignals := []struct {
		name       string
		confidence int
	}{
		{name: "pom.xml", confidence: 55},
		{name: "build.gradle", confidence: 45},
		{name: "build.gradle.kts", confidence: 45},
	}
	for _, signal := range rootSignals {
		path := filepath.Join(repoPath, signal.name)
		if _, err := os.Stat(path); err == nil {
			detection.Matched = true
			detection.Confidence += signal.confidence
			roots[repoPath] = struct{}{}
		} else if !os.IsNotExist(err) {
			return language.Detection{}, err
		}
	}

	const maxFiles = 1024
	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}

		visited++
		if visited > maxFiles {
			return fs.SkipAll
		}

		switch strings.ToLower(entry.Name()) {
		case "pom.xml", "build.gradle", "build.gradle.kts":
			detection.Matched = true
			detection.Confidence += 10
			roots[filepath.Dir(path)] = struct{}{}
		}

		switch strings.ToLower(filepath.Ext(path)) {
		case ".java", ".kt", ".kts":
			detection.Matched = true
			detection.Confidence += 2
		}

		return nil
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	if detection.Matched && detection.Confidence < 35 {
		detection.Confidence = 35
	}
	if detection.Confidence > 95 {
		detection.Confidence = 95
	}
	if len(roots) == 0 && detection.Matched {
		roots[repoPath] = struct{}{}
	}
	detection.Roots = sortedKeys(roots)
	return detection, nil
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	result := report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}

	declaredDependencies, depPrefixes, depAliases := collectDeclaredDependencies(repoPath)
	scanResult, err := scanRepo(ctx, repoPath, depPrefixes, depAliases)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		depReport, warnings := buildDependencyReport(dependency, scanResult)
		result.Dependencies = []report.DependencyReport{depReport}
		result.Warnings = append(result.Warnings, warnings...)
		result.Summary = report.ComputeSummary(result.Dependencies)
	case req.TopN > 0:
		dependencies := listDependencies(scanResult)
		reports := make([]report.DependencyReport, 0, len(dependencies))
		for _, dependency := range dependencies {
			depReport, warnings := buildDependencyReport(dependency, scanResult)
			reports = append(reports, depReport)
			result.Warnings = append(result.Warnings, warnings...)
		}
		sort.Slice(reports, func(i, j int) bool {
			iScore, iKnown := wasteScore(reports[i])
			jScore, jKnown := wasteScore(reports[j])
			if iKnown != jKnown {
				return iKnown
			}
			if iScore == jScore {
				return reports[i].Name < reports[j].Name
			}
			return iScore > jScore
		})
		if req.TopN > 0 && req.TopN < len(reports) {
			reports = reports[:req.TopN]
		}
		result.Dependencies = reports
		if len(reports) == 0 {
			result.Warnings = append(result.Warnings, "no dependency data available for top-N ranking")
		}
		result.Summary = report.ComputeSummary(result.Dependencies)
	default:
		result.Warnings = append(result.Warnings, "no dependency or top-N target provided")
	}

	if len(declaredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no JVM dependencies discovered from pom.xml or build.gradle manifests")
	}

	return result, nil
}

type importBinding struct {
	Dependency string
	Module     string
	Name       string
	Local      string
	Location   report.Location
	Wildcard   bool
}

type fileScan struct {
	Path    string
	Package string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files    []fileScan
	Warnings []string
}

func scanRepo(ctx context.Context, repoPath string, depPrefixes map[string]string, depAliases map[string]string) (scanResult, error) {
	result := scanResult{}
	if repoPath == "" {
		return result, fs.ErrInvalid
	}

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSourceFile(path) {
			return nil
		}
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			return err
		}
		relativePath, err := filepath.Rel(repoPath, path)
		if err != nil {
			relativePath = path
		}

		filePackage := parsePackage(content)
		imports := parseImports(content, relativePath, filePackage, depPrefixes, depAliases)
		result.Files = append(result.Files, fileScan{
			Path:    relativePath,
			Package: filePackage,
			Imports: imports,
			Usage:   countUsage(content, imports),
		})
		return nil
	})
	if err != nil {
		return result, err
	}

	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Java/Kotlin source files found for analysis")
	}
	return result, nil
}

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt", ".kts":
		return true
	default:
		return false
	}
}

var (
	packagePattern = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][A-Za-z0-9_\.]*)\s*;?\s*$`)
	importPattern  = regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([A-Za-z_][A-Za-z0-9_\.]*)(\.\*)?\s*;?\s*$`)
)

func parsePackage(content []byte) string {
	matches := packagePattern.FindSubmatch(content)
	if len(matches) != 2 {
		return ""
	}
	return strings.TrimSpace(string(matches[1]))
}

func parseImports(content []byte, filePath string, filePackage string, depPrefixes map[string]string, depAliases map[string]string) []importBinding {
	lines := strings.Split(string(content), "\n")
	imports := make([]importBinding, 0)
	for index, line := range lines {
		line = stripLineComment(line)
		matches := importPattern.FindStringSubmatch(line)
		if len(matches) != 3 {
			continue
		}
		module := strings.TrimSpace(matches[1])
		if module == "" || shouldIgnoreImport(module, filePackage) {
			continue
		}

		dependency := resolveDependency(module, depPrefixes, depAliases)
		if dependency == "" {
			dependency = fallbackDependency(module)
		}
		if dependency == "" {
			continue
		}

		wildcard := strings.TrimSpace(matches[2]) == ".*"
		symbol := lastModuleSegment(module)
		if wildcard {
			symbol = "*"
		}
		if symbol == "" {
			continue
		}

		imports = append(imports, importBinding{
			Dependency: dependency,
			Module:     module,
			Name:       symbol,
			Local:      symbol,
			Wildcard:   wildcard,
			Location: report.Location{
				File:   filePath,
				Line:   index + 1,
				Column: firstContentColumn(line),
			},
		})
	}
	return imports
}

func stripLineComment(line string) string {
	if index := strings.Index(line, "//"); index >= 0 {
		return line[:index]
	}
	return line
}

func shouldIgnoreImport(module string, filePackage string) bool {
	module = strings.TrimSpace(module)
	if module == "" {
		return true
	}

	stdlibPrefixes := []string{
		"java.", "javax.", "kotlin.", "jdk.", "sun.",
	}
	for _, prefix := range stdlibPrefixes {
		if strings.HasPrefix(module, prefix) {
			return true
		}
	}

	if filePackage != "" {
		if module == filePackage || strings.HasPrefix(module, filePackage+".") {
			return true
		}
	}
	return false
}

func resolveDependency(module string, depPrefixes map[string]string, depAliases map[string]string) string {
	best := ""
	bestLen := 0

	for prefix, dependency := range depPrefixes {
		if module == prefix || strings.HasPrefix(module, prefix+".") {
			if len(prefix) > bestLen {
				best = dependency
				bestLen = len(prefix)
			}
		}
	}
	if best != "" {
		return best
	}

	parts := strings.Split(module, ".")
	for i := len(parts); i >= 1; i-- {
		key := strings.Join(parts[:i], ".")
		if dependency, ok := depAliases[key]; ok {
			return dependency
		}
	}

	return ""
}

func fallbackDependency(module string) string {
	parts := strings.Split(module, ".")
	if len(parts) >= 2 {
		return normalizeDependencyID(parts[0] + "." + parts[1])
	}
	if len(parts) == 1 {
		return normalizeDependencyID(parts[0])
	}
	return ""
}

func lastModuleSegment(module string) string {
	parts := strings.Split(module, ".")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

func firstContentColumn(line string) int {
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' {
			return i + 1
		}
	}
	return 1
}

func countUsage(content []byte, imports []importBinding) map[string]int {
	importCount := make(map[string]int)
	for _, item := range imports {
		if item.Wildcard || item.Local == "" {
			continue
		}
		importCount[item.Local]++
	}

	usage := make(map[string]int, len(importCount))
	text := string(content)
	for local, count := range importCount {
		pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(local) + `\b`)
		occurrences := len(pattern.FindAllStringIndex(text, -1)) - count
		if occurrences < 0 {
			occurrences = 0
		}
		usage[local] = occurrences
	}
	return usage
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	usedImports := make(map[string]*report.ImportUse)
	unusedImports := make(map[string]*report.ImportUse)
	usedSymbols := make(map[string]struct{})
	allSymbols := make(map[string]struct{})
	symbolCounts := make(map[string]int)
	var warnings []string
	wildcardImports := 0

	for _, file := range scan.Files {
		for _, imported := range file.Imports {
			if normalizeDependencyID(imported.Dependency) != dependency {
				continue
			}

			allSymbols[imported.Name] = struct{}{}
			used := imported.Wildcard || file.Usage[imported.Local] > 0
			if imported.Wildcard {
				wildcardImports++
			}
			entry := report.ImportUse{
				Name:   imported.Name,
				Module: imported.Module,
				Locations: []report.Location{
					imported.Location,
				},
			}
			if used {
				usedSymbols[imported.Name] = struct{}{}
				count := file.Usage[imported.Local]
				if imported.Wildcard && count == 0 {
					count = 1
				}
				if count > 0 {
					symbolCounts[imported.Name] += count
				}
				addImport(usedImports, entry)
			} else {
				addImport(unusedImports, entry)
			}
		}
	}

	if len(allSymbols) == 0 {
		warnings = append(warnings, "no imports found for dependency "+dependency)
	}

	usedCount := len(usedSymbols)
	totalCount := len(allSymbols)
	usedPercent := 0.0
	if totalCount > 0 {
		usedPercent = (float64(usedCount) / float64(totalCount)) * 100
	}

	topSymbols := make([]report.SymbolUsage, 0, len(symbolCounts))
	for name, count := range symbolCounts {
		topSymbols = append(topSymbols, report.SymbolUsage{Name: name, Count: count})
	}
	sort.Slice(topSymbols, func(i, j int) bool {
		if topSymbols[i].Count == topSymbols[j].Count {
			return topSymbols[i].Name < topSymbols[j].Name
		}
		return topSymbols[i].Count > topSymbols[j].Count
	})
	if len(topSymbols) > 5 {
		topSymbols = topSymbols[:5]
	}

	dep := report.DependencyReport{
		Language:             "jvm",
		Name:                 dependency,
		UsedExportsCount:     usedCount,
		TotalExportsCount:    totalCount,
		UsedPercent:          usedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       topSymbols,
		UsedImports:          flattenImports(usedImports),
		UnusedImports:        dedupeUnused(flattenImports(unusedImports), flattenImports(usedImports)),
	}
	if wildcardImports > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "wildcard-import",
			Severity: "medium",
			Message:  "found wildcard imports for this dependency",
		})
	}
	dep.Recommendations = buildRecommendations(dep)
	return dep, warnings
}

func listDependencies(scan scanResult) []string {
	set := make(map[string]struct{})
	for _, file := range scan.Files {
		for _, imported := range file.Imports {
			if imported.Dependency == "" {
				continue
			}
			set[normalizeDependencyID(imported.Dependency)] = struct{}{}
		}
	}
	items := make([]string, 0, len(set))
	for item := range set {
		items = append(items, item)
	}
	sort.Strings(items)
	return items
}

func addImport(dest map[string]*report.ImportUse, entry report.ImportUse) {
	key := entry.Module + ":" + entry.Name
	if current, ok := dest[key]; ok {
		current.Locations = append(current.Locations, entry.Locations...)
		return
	}
	copyEntry := entry
	dest[key] = &copyEntry
}

func flattenImports(source map[string]*report.ImportUse) []report.ImportUse {
	items := make([]report.ImportUse, 0, len(source))
	for _, entry := range source {
		items = append(items, *entry)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Module == items[j].Module {
			return items[i].Name < items[j].Name
		}
		return items[i].Module < items[j].Module
	})
	return items
}

func dedupeUnused(unused []report.ImportUse, used []report.ImportUse) []report.ImportUse {
	usedKeys := make(map[string]struct{}, len(used))
	for _, entry := range used {
		usedKeys[entry.Module+":"+entry.Name] = struct{}{}
	}
	filtered := make([]report.ImportUse, 0, len(unused))
	for _, entry := range unused {
		if _, ok := usedKeys[entry.Module+":"+entry.Name]; ok {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func buildRecommendations(dep report.DependencyReport) []report.Recommendation {
	recommendations := make([]report.Recommendation, 0, 2)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   "No used imports were detected for this dependency; consider removing it.",
			Rationale: "Unused dependencies increase attack and maintenance surface.",
		})
	}
	if hasWildcardImport(dep.UsedImports) || hasWildcardImport(dep.UnusedImports) {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "avoid-wildcard-imports",
			Priority:  "medium",
			Message:   "Wildcard imports were detected; prefer explicit imports.",
			Rationale: "Explicit imports improve analysis precision and maintainability.",
		})
	}
	return recommendations
}

func hasWildcardImport(imports []report.ImportUse) bool {
	for _, imported := range imports {
		if imported.Name == "*" {
			return true
		}
	}
	return false
}

func wasteScore(dep report.DependencyReport) (float64, bool) {
	if dep.TotalExportsCount == 0 {
		return -1, false
	}
	return 100 - dep.UsedPercent, true
}

func normalizeDependencyID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".idea", "node_modules", "build", "target", ".gradle", ".mvn", "out", ".classpath", ".settings":
		return true
	default:
		return false
	}
}

func sortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

type dependencyDescriptor struct {
	Name     string
	Group    string
	Artifact string
}

func collectDeclaredDependencies(repoPath string) ([]dependencyDescriptor, map[string]string, map[string]string) {
	descriptors := make([]dependencyDescriptor, 0)

	pomDescriptors, gradleDescriptors := parsePomDependencies(repoPath), parseGradleDependencies(repoPath)
	descriptors = append(descriptors, pomDescriptors...)
	descriptors = append(descriptors, gradleDescriptors...)

	unique := make(map[string]dependencyDescriptor)
	for _, descriptor := range descriptors {
		key := descriptor.Group + ":" + descriptor.Artifact
		if descriptor.Group == "" {
			key = descriptor.Name
		}
		unique[key] = descriptor
	}
	descriptors = make([]dependencyDescriptor, 0, len(unique))
	for _, descriptor := range unique {
		descriptors = append(descriptors, descriptor)
	}
	sort.Slice(descriptors, func(i, j int) bool {
		if descriptors[i].Name == descriptors[j].Name {
			return descriptors[i].Group < descriptors[j].Group
		}
		return descriptors[i].Name < descriptors[j].Name
	})

	prefixes := make(map[string]string)
	aliases := make(map[string]string)
	for _, descriptor := range descriptors {
		name := normalizeDependencyID(descriptor.Name)
		if descriptor.Group != "" {
			group := strings.TrimSpace(descriptor.Group)
			prefixes[group] = name
			aliases[group] = name
			parts := strings.Split(group, ".")
			if len(parts) >= 2 {
				aliases[parts[0]+"."+parts[1]] = name
				aliases[parts[len(parts)-1]] = name
			}
		}
		if descriptor.Artifact != "" {
			artifact := strings.ReplaceAll(strings.TrimSpace(descriptor.Artifact), "-", ".")
			if descriptor.Group != "" && artifact != "" {
				prefixes[descriptor.Group+"."+artifact] = name
			}
			if artifact != "" {
				aliases[artifact] = name
			}
		}
	}
	return descriptors, prefixes, aliases
}

func parsePomDependencies(repoPath string) []dependencyDescriptor {
	pattern := regexp.MustCompile(`(?s)<dependency>\s*.*?<groupId>\s*([^<\s]+)\s*</groupId>\s*.*?<artifactId>\s*([^<\s]+)\s*</artifactId>.*?</dependency>`)
	return parseBuildFiles(repoPath, "pom.xml", func(content string) []dependencyDescriptor {
		matches := pattern.FindAllStringSubmatch(content, -1)
		descriptors := make([]dependencyDescriptor, 0, len(matches))
		for _, match := range matches {
			if len(match) != 3 {
				continue
			}
			group := strings.TrimSpace(match[1])
			artifact := strings.TrimSpace(match[2])
			if group == "" || artifact == "" {
				continue
			}
			descriptors = append(descriptors, dependencyDescriptor{
				Name:     artifact,
				Group:    group,
				Artifact: artifact,
			})
		}
		return descriptors
	})
}

func parseGradleDependencies(repoPath string) []dependencyDescriptor {
	pattern := regexp.MustCompile(`(?m)(?:implementation|api|compileOnly|runtimeOnly|testImplementation|testRuntimeOnly|kapt)\s*\(?\s*["']([^:"'\s]+):([^:"'\s]+):[^"'\s]+["']\s*\)?`)
	return parseBuildFiles(repoPath, "build.gradle", func(content string) []dependencyDescriptor {
		return parseGradleMatches(content, pattern)
	}, "build.gradle.kts")
}

func parseGradleMatches(content string, pattern *regexp.Regexp) []dependencyDescriptor {
	matches := pattern.FindAllStringSubmatch(content, -1)
	descriptors := make([]dependencyDescriptor, 0, len(matches))
	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		group := strings.TrimSpace(match[1])
		artifact := strings.TrimSpace(match[2])
		if group == "" || artifact == "" {
			continue
		}
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     artifact,
			Group:    group,
			Artifact: artifact,
		})
	}
	return descriptors
}

func parseBuildFiles(repoPath string, primaryName string, parser func(content string) []dependencyDescriptor, additionalNames ...string) []dependencyDescriptor {
	names := append([]string{primaryName}, additionalNames...)
	descriptors := make([]dependencyDescriptor, 0)
	seen := make(map[string]struct{})

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		fileName := strings.ToLower(entry.Name())
		matched := false
		for _, name := range names {
			if fileName == strings.ToLower(name) {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}

		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			return nil
		}
		for _, descriptor := range parser(string(content)) {
			key := descriptor.Group + ":" + descriptor.Artifact
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			descriptors = append(descriptors, descriptor)
		}
		return nil
	})
	if err != nil {
		return descriptors
	}
	return descriptors
}
