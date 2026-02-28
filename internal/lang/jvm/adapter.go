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

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	Clock func() time.Time
}

const (
	pomXMLName         = "pom.xml"
	buildGradleName    = "build.gradle"
	buildGradleKTSName = "build.gradle.kts"
)

var jvmSkippedDirectories = map[string]bool{
	"target":     true,
	".gradle":    true,
	".mvn":       true,
	"out":        true,
	".classpath": true,
	".settings":  true,
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
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	_ = ctx
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})
	if err := applyJVMRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	const maxFiles = 1024
	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return walkJVMDetectionEntry(path, entry, roots, &detection, &visited, maxFiles)
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func walkJVMDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	(*visited)++
	if *visited > maxFiles {
		return fs.SkipAll
	}
	updateJVMDetection(path, entry, roots, detection)
	return nil
}

func applyJVMRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	rootSignals := []struct {
		name       string
		confidence int
	}{
		{name: pomXMLName, confidence: 55},
		{name: buildGradleName, confidence: 45},
		{name: buildGradleKTSName, confidence: 45},
	}
	for _, signal := range rootSignals {
		path := filepath.Join(repoPath, signal.name)
		if _, err := os.Stat(path); err == nil {
			detection.Matched = true
			detection.Confidence += signal.confidence
			roots[repoPath] = struct{}{}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func updateJVMDetection(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection) {
	switch strings.ToLower(entry.Name()) {
	case pomXMLName, buildGradleName, buildGradleKTSName:
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt", ".kts":
		detection.Matched = true
		detection.Confidence += 2
		if root := sourceLayoutModuleRoot(path); root != "" {
			roots[root] = struct{}{}
		}
	}
}

func sourceLayoutModuleRoot(path string) string {
	normalized := filepath.ToSlash(filepath.Clean(path))
	index := strings.Index(normalized, "/src/")
	if index < 0 {
		return ""
	}

	segments := strings.Split(normalized[index+len("/src/"):], "/")
	if len(segments) < 2 {
		return ""
	}
	switch segments[1] {
	case "java", "kotlin":
		return filepath.FromSlash(normalized[:index])
	default:
		return ""
	}
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

	dependencies, warnings := buildRequestedJVMDependencies(req, scanResult)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	if len(declaredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no JVM dependencies discovered from pom.xml or build.gradle manifests")
	}

	return result, nil
}

func buildRequestedJVMDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, resolveRemovalCandidateWeights, buildTopJVMDependencies)
}

func buildTopJVMDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	fileUsages := shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
	dependencies := shared.ListDependencies(fileUsages, normalizeDependencyID)
	reportBuilder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	return shared.BuildTopReports(topN, dependencies, reportBuilder, weights)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

type importBinding = shared.ImportRecord

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
		return scanJVMSourceFile(repoPath, path, depPrefixes, depAliases, &result)
	})
	if err != nil {
		return result, err
	}

	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Java/Kotlin source files found for analysis")
	}
	return result, nil
}

func scanJVMSourceFile(repoPath string, path string, depPrefixes map[string]string, depAliases map[string]string, result *scanResult) error {
	if !isSourceFile(path) {
		return nil
	}
	var (
		content []byte
		err     error
	)
	if strings.TrimSpace(repoPath) == "" {
		content, err = safeio.ReadFile(path)
	} else {
		content, err = safeio.ReadFileUnder(repoPath, path)
	}
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
	importPattern  = regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([A-Za-z_][A-Za-z0-9_\.]*)(\.\*)?(?:\s+as\s+([A-Za-z_][A-Za-z0-9_]*))?\s*;?\s*$`)
)

const importPatternMatchGroups = 4

func parsePackage(content []byte) string {
	matches := packagePattern.FindSubmatch(content)
	if len(matches) != 2 {
		return ""
	}
	return strings.TrimSpace(string(matches[1]))
}

func parseImports(content []byte, filePath string, filePackage string, depPrefixes map[string]string, depAliases map[string]string) []importBinding {
	return shared.ParseImportLines(content, filePath, func(line string, _ int) []shared.ImportRecord {
		line = stripLineComment(line)
		matches := importPattern.FindStringSubmatch(line)
		if len(matches) != importPatternMatchGroups {
			return nil
		}
		module := strings.TrimSpace(matches[1])
		if module == "" || shouldIgnoreImport(module, filePackage) {
			return nil
		}

		dependency := resolveDependency(module, depPrefixes, depAliases)
		if dependency == "" {
			dependency = fallbackDependency(module)
		}
		if dependency == "" {
			return nil
		}

		record, ok := buildImportRecord(matches, module, dependency)
		if !ok {
			return nil
		}

		return []shared.ImportRecord{record}
	})
}

func buildImportRecord(matches []string, module string, dependency string) (shared.ImportRecord, bool) {
	wildcard := strings.TrimSpace(matches[2]) == ".*"
	symbol := lastModuleSegment(module)
	if wildcard {
		symbol = "*"
	}
	if symbol == "" {
		return shared.ImportRecord{}, false
	}

	localName := symbol
	if alias := strings.TrimSpace(matches[3]); alias != "" && !wildcard {
		localName = alias
	}

	return shared.ImportRecord{
		Dependency: dependency,
		Module:     module,
		Name:       symbol,
		Local:      localName,
		Wildcard:   wildcard,
	}, true
}

func stripLineComment(line string) string {
	return shared.StripLineComment(line, "//")
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
	return shared.FirstContentColumn(line)
}

func countUsage(content []byte, imports []importBinding) map[string]int {
	return shared.CountUsage(content, imports)
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	fileUsages := shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
	stats := shared.BuildDependencyStats(dependency, fileUsages, normalizeDependencyID)
	warnings := make([]string, 0)
	if !stats.HasImports {
		warnings = append(warnings, "no imports found for dependency "+dependency)
	}

	dep := report.DependencyReport{
		Language:             "jvm",
		Name:                 dependency,
		UsedExportsCount:     stats.UsedCount,
		TotalExportsCount:    stats.TotalCount,
		UsedPercent:          stats.UsedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       stats.TopSymbols,
		UsedImports:          stats.UsedImports,
		UnusedImports:        stats.UnusedImports,
	}
	if stats.WildcardImports > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "wildcard-import",
			Severity: "medium",
			Message:  "found wildcard imports for this dependency",
		})
	}
	dep.Recommendations = buildRecommendations(dep)
	return dep, warnings
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
	if shared.HasWildcardImport(dep.UsedImports) || shared.HasWildcardImport(dep.UnusedImports) {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "avoid-wildcard-imports",
			Priority:  "medium",
			Message:   "Wildcard imports were detected; prefer explicit imports.",
			Rationale: "Explicit imports improve analysis precision and maintainability.",
		})
	}
	return recommendations
}

func normalizeDependencyID(value string) string {
	return shared.NormalizeDependencyID(value)
}

func shouldSkipDir(name string) bool {
	return shared.ShouldSkipDir(name, jvmSkippedDirectories)
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

	descriptors = dedupeAndSortDescriptors(descriptors)
	prefixes, aliases := buildDescriptorLookups(descriptors)
	return descriptors, prefixes, aliases
}

func dedupeAndSortDescriptors(descriptors []dependencyDescriptor) []dependencyDescriptor {
	unique := make(map[string]dependencyDescriptor)
	for _, descriptor := range descriptors {
		key := descriptor.Group + ":" + descriptor.Artifact
		if descriptor.Group == "" {
			key = descriptor.Name
		}
		unique[key] = descriptor
	}
	items := make([]dependencyDescriptor, 0, len(unique))
	for _, descriptor := range unique {
		items = append(items, descriptor)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].Group < items[j].Group
		}
		return items[i].Name < items[j].Name
	})
	return items
}

func buildDescriptorLookups(descriptors []dependencyDescriptor) (map[string]string, map[string]string) {
	prefixes := make(map[string]string)
	aliases := make(map[string]string)
	for _, descriptor := range descriptors {
		name := normalizeDependencyID(descriptor.Name)
		addGroupLookups(prefixes, aliases, name, descriptor.Group)
		addArtifactLookups(prefixes, aliases, name, descriptor.Group, descriptor.Artifact)
	}
	return prefixes, aliases
}

type lookupKeyStrategy func(group string, artifact string) ([]string, []string)

func addGroupLookups(prefixes map[string]string, aliases map[string]string, name string, group string) {
	addLookupByStrategy(prefixes, aliases, name, group, "", groupLookupStrategy)
}

func addArtifactLookups(prefixes map[string]string, aliases map[string]string, name string, group string, artifact string) {
	addLookupByStrategy(prefixes, aliases, name, group, artifact, artifactLookupStrategy)
}

func addLookupByStrategy(prefixes map[string]string, aliases map[string]string, name string, group string, artifact string, strategy lookupKeyStrategy) {
	prefixKeys, aliasKeys := strategy(group, artifact)
	for _, key := range prefixKeys {
		prefixes[key] = name
	}
	for _, key := range aliasKeys {
		aliases[key] = name
	}
}

func groupLookupStrategy(group string, _ string) ([]string, []string) {
	if group == "" {
		return nil, nil
	}
	group = strings.TrimSpace(group)
	prefixes := []string{group}
	aliases := []string{group}
	parts := strings.Split(group, ".")
	if len(parts) >= 2 {
		aliases = append(aliases, parts[0]+"."+parts[1], parts[len(parts)-1])
	}
	return prefixes, aliases
}

func artifactLookupStrategy(group string, artifact string) ([]string, []string) {
	if artifact == "" {
		return nil, nil
	}
	artifact = strings.ReplaceAll(strings.TrimSpace(artifact), "-", ".")
	prefixes := make([]string, 0, 1)
	aliases := make([]string, 0, 1)
	if group != "" && artifact != "" {
		prefixes = append(prefixes, group+"."+artifact)
	}
	if artifact != "" {
		aliases = append(aliases, artifact)
	}
	return prefixes, aliases
}

func parsePomDependencies(repoPath string) []dependencyDescriptor {
	pattern := regexp.MustCompile(`(?s)<dependency>\s*.*?<groupId>\s*([^<\s]+)\s*</groupId>\s*.*?<artifactId>\s*([^<\s]+)\s*</artifactId>.*?</dependency>`)
	return parseBuildFiles(repoPath, pomXMLName, func(content string) []dependencyDescriptor {
		return parseDependencyDescriptorsFromMatches(pattern.FindAllStringSubmatch(content, -1))
	})
}

func parseGradleDependencies(repoPath string) []dependencyDescriptor {
	pattern := regexp.MustCompile(`(?m)(?:implementation|api|compileOnly|runtimeOnly|testImplementation|testRuntimeOnly|kapt)\s*\(?\s*["']([^:"'\s]+):([^:"'\s]+):[^"'\s]+["']\s*\)?`)
	gradleParser := func(content string) []dependencyDescriptor {
		return parseGradleMatches(content, pattern)
	}
	return parseBuildFiles(repoPath, buildGradleName, gradleParser, buildGradleKTSName)
}

func parseGradleMatches(content string, pattern *regexp.Regexp) []dependencyDescriptor {
	return parseDependencyDescriptorsFromMatches(pattern.FindAllStringSubmatch(content, -1))
}

func parseDependencyDescriptorsFromMatches(matches [][]string) []dependencyDescriptor {
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
		return parseBuildFileEntry(repoPath, path, entry, names, parser, seen, &descriptors)
	})
	if err != nil {
		return descriptors
	}
	return descriptors
}

func parseBuildFileEntry(repoPath string, path string, entry fs.DirEntry, names []string, parser func(content string) []dependencyDescriptor, seen map[string]struct{}, descriptors *[]dependencyDescriptor) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	fileName := strings.ToLower(entry.Name())
	if !matchesBuildFile(fileName, names) {
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
		*descriptors = append(*descriptors, descriptor)
	}
	return nil
}

func matchesBuildFile(fileName string, names []string) bool {
	for _, name := range names {
		if fileName == strings.ToLower(name) {
			return true
		}
	}
	return false
}
