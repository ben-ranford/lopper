package kotlinandroid

import (
	"context"
	"fmt"
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
	buildGradleName     = "build.gradle"
	buildGradleKTSName  = "build.gradle.kts"
	settingsGradleName  = "settings.gradle"
	settingsGradleKTS   = "settings.gradle.kts"
	gradleLockfileName  = "gradle.lockfile"
	androidManifestName = "androidmanifest.xml"
)

var kotlinAndroidSkippedDirectories = map[string]bool{
	".gradle":    true,
	"build":      true,
	"out":        true,
	"target":     true,
	".classpath": true,
	".settings":  true,
}

var androidBuildPluginMarkers = []string{
	"com.android.application",
	"com.android.dynamic-feature",
	"com.android.library",
	"com.android.test",
	"org.jetbrains.kotlin.android",
}

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "kotlin-android"
}

func (a *Adapter) Aliases() []string {
	return []string{"android-kotlin", "gradle-android", "android"}
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})
	androidSpecificSignal := false
	if err := applyKotlinAndroidRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	const maxFiles = 1200
	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return walkKotlinAndroidDetectionEntry(path, entry, roots, &detection, &visited, maxFiles, &androidSpecificSignal)
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	if !androidSpecificSignal {
		detection.Matched = false
		clear(roots)
	}
	pruneKotlinAndroidRoots(repoPath, roots)
	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func walkKotlinAndroidDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int, androidSpecificSignal *bool) error {
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
	updateKotlinAndroidDetection(path, entry, roots, detection, androidSpecificSignal)
	return nil
}

func applyKotlinAndroidRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	signals := []shared.RootSignal{
		{Name: buildGradleName, Confidence: 45},
		{Name: buildGradleKTSName, Confidence: 45},
		{Name: settingsGradleName, Confidence: 30},
		{Name: settingsGradleKTS, Confidence: 30},
		{Name: gradleLockfileName, Confidence: 25},
	}
	return shared.ApplyRootSignals(repoPath, signals, detection, roots)
}

func updateKotlinAndroidDetection(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, androidSpecificSignal *bool) {
	name := strings.ToLower(entry.Name())
	switch name {
	case buildGradleName, buildGradleKTSName:
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
		if buildFileSignalsAndroidPlugin(path) {
			markAndroidSpecificDetection(detection, androidSpecificSignal)
		}
	case settingsGradleName, settingsGradleKTS:
		detection.Matched = true
		detection.Confidence += 8
		roots[filepath.Dir(path)] = struct{}{}
	case gradleLockfileName:
		detection.Matched = true
		detection.Confidence += 10
		roots[filepath.Dir(path)] = struct{}{}
	case androidManifestName:
		markAndroidSpecificDetection(detection, androidSpecificSignal)
		if root := androidManifestModuleRoot(path); root != "" {
			roots[root] = struct{}{}
		} else {
			roots[filepath.Dir(path)] = struct{}{}
		}
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt":
		detection.Matched = true
		detection.Confidence += 2
		if root := sourceLayoutModuleRoot(path); root != "" {
			roots[root] = struct{}{}
		}
	}
}

func markAndroidSpecificDetection(detection *language.Detection, androidSpecificSignal *bool) {
	detection.Matched = true
	detection.Confidence += 18
	if androidSpecificSignal != nil {
		*androidSpecificSignal = true
	}
}

func buildFileSignalsAndroidPlugin(path string) bool {
	content, err := safeio.ReadFile(path)
	if err != nil {
		return false
	}
	buildFile := strings.ToLower(string(content))
	for _, marker := range androidBuildPluginMarkers {
		if strings.Contains(buildFile, marker) {
			return true
		}
	}
	return false
}

func androidManifestModuleRoot(path string) string {
	normalized := filepath.ToSlash(filepath.Clean(path))
	if normalized == "" {
		return ""
	}
	parts := strings.Split(normalized, "/")
	if len(parts) < 4 {
		return ""
	}
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] != "src" || parts[i+1] != "main" {
			continue
		}
		if strings.ToLower(parts[i+2]) != "androidmanifest.xml" {
			continue
		}
		if i == 0 {
			return ""
		}
		root := strings.Join(parts[:i], "/")
		if root == "" {
			return ""
		}
		return filepath.FromSlash(root)
	}
	return ""
}

func sourceLayoutModuleRoot(path string) string {
	normalized := filepath.ToSlash(filepath.Clean(path))
	if normalized == "" {
		return ""
	}
	parts := strings.Split(normalized, "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] != "src" || parts[i+1] != "main" {
			continue
		}
		if !isAndroidSourceLayout(parts[i+2]) {
			continue
		}
		root := strings.Join(parts[:i], "/")
		if root == "" {
			return ""
		}
		return filepath.FromSlash(root)
	}
	return ""
}

func isAndroidSourceLayout(name string) bool {
	return name == "java" || name == "kotlin"
}

func pruneKotlinAndroidRoots(repoPath string, roots map[string]struct{}) {
	if len(roots) <= 1 {
		return
	}
	repoPath = filepath.Clean(repoPath)
	if _, ok := roots[repoPath]; !ok {
		return
	}
	hasNestedModuleRoot := false
	for root := range roots {
		if filepath.Clean(root) == repoPath {
			continue
		}
		if !isSubPath(repoPath, root) {
			continue
		}
		if !hasGradleBuildFile(root) {
			continue
		}
		hasNestedModuleRoot = true
		break
	}
	if !hasNestedModuleRoot {
		return
	}
	if shouldKeepRepoRootForPackageAnalysis(repoPath) {
		return
	}
	delete(roots, repoPath)
}

func shouldKeepRepoRootForPackageAnalysis(repoPath string) bool {
	if !hasGradleBuildFile(repoPath) {
		return false
	}
	if hasRootGradleDependencyDeclarations(repoPath) {
		return true
	}
	return hasRootSourceLayout(repoPath)
}

func hasRootGradleDependencyDeclarations(repoPath string) bool {
	for _, fileName := range []string{buildGradleName, buildGradleKTSName} {
		path := filepath.Join(repoPath, fileName)
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			continue
		}
		if len(parseGradleDependencyContent(string(content))) > 0 {
			return true
		}
	}
	return false
}

func hasRootSourceLayout(repoPath string) bool {
	srcRoot := filepath.Join(repoPath, "src")
	found := false
	err := filepath.WalkDir(srcRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isSourceFile(path) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return false
	}
	return found
}

func hasGradleBuildFile(root string) bool {
	for _, fileName := range []string{buildGradleName, buildGradleKTSName} {
		path := filepath.Join(root, fileName)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			return true
		}
	}
	return false
}

func isSubPath(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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

	descriptors, lookups, declarationWarnings := collectDeclaredDependencies(repoPath)
	result.Warnings = append(result.Warnings, declarationWarnings...)

	scanResult, err := scanRepo(ctx, repoPath, lookups)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	dependencies, warnings := buildRequestedKotlinAndroidDependencies(req, scanResult)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	if len(descriptors) == 0 {
		result.Warnings = append(result.Warnings, "no Kotlin/Android dependencies discovered from Gradle manifests")
	}
	if !lookups.HasLockfile {
		result.Warnings = append(result.Warnings, "gradle.lockfile not found; dependency versions may be incomplete")
	}

	return result, nil
}

func buildRequestedKotlinAndroidDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, resolveRemovalCandidateWeights, buildTopKotlinAndroidDependencies)
}

func buildTopKotlinAndroidDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	reportBuilder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	dependencies := shared.ListDependencies(kotlinAndroidFileUsages(scan), normalizeDependencyID)
	return shared.BuildTopReports(topN, dependencies, reportBuilder, weights)
}

func kotlinAndroidFileUsages(scan scanResult) []shared.FileUsage {
	importsOf := func(file fileScan) []shared.ImportRecord { return file.Imports }
	usageOf := func(file fileScan) map[string]int { return file.Usage }
	return shared.MapFileUsages(scan.Files, importsOf, usageOf)
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
	Files                  []fileScan
	Warnings               []string
	AmbiguousDependencies  map[string]struct{}
	UndeclaredDependencies map[string]struct{}

	fallbackModules  map[string]string
	ambiguousModules map[string][]string
}

func newScanResult() scanResult {
	return scanResult{
		AmbiguousDependencies:  make(map[string]struct{}),
		UndeclaredDependencies: make(map[string]struct{}),
		fallbackModules:        make(map[string]string),
		ambiguousModules:       make(map[string][]string),
	}
}

func (s *scanResult) addFallbackModule(module string, dependency string, declared bool) {
	module = strings.TrimSpace(module)
	if module == "" {
		return
	}
	if _, ok := s.fallbackModules[module]; !ok {
		s.fallbackModules[module] = dependency
	}
	if !declared {
		s.UndeclaredDependencies[normalizeDependencyID(dependency)] = struct{}{}
	}
}

func (s *scanResult) addAmbiguousModule(module string, candidates []string, chosen string) {
	module = strings.TrimSpace(module)
	if module == "" {
		return
	}
	if _, ok := s.ambiguousModules[module]; !ok {
		s.ambiguousModules[module] = append([]string{}, candidates...)
	}
	s.AmbiguousDependencies[normalizeDependencyID(chosen)] = struct{}{}
}

func (s *scanResult) appendInferenceWarnings() {
	if len(s.fallbackModules) > 0 {
		examples := make([]string, 0, len(s.fallbackModules))
		for module, dependency := range s.fallbackModules {
			examples = append(examples, module+" -> "+dependency)
		}
		sort.Strings(examples)
		if len(examples) > 3 {
			examples = examples[:3]
		}
		warning := fmt.Sprintf("%d import(s) were conservatively attributed because no declared Gradle mapping matched (examples: %s)", len(s.fallbackModules), strings.Join(examples, "; "))
		s.Warnings = append(s.Warnings, warning)
	}

	if len(s.UndeclaredDependencies) > 0 {
		undeclared := make([]string, 0, len(s.UndeclaredDependencies))
		for dependency := range s.UndeclaredDependencies {
			undeclared = append(undeclared, dependency)
		}
		sort.Strings(undeclared)
		s.Warnings = append(s.Warnings, "imports were attributed to undeclared dependencies: "+strings.Join(undeclared, ", "))
	}

	if len(s.ambiguousModules) > 0 {
		examples := make([]string, 0, len(s.ambiguousModules))
		for module, candidates := range s.ambiguousModules {
			examples = append(examples, module+" -> "+strings.Join(candidates, "|"))
		}
		sort.Strings(examples)
		if len(examples) > 3 {
			examples = examples[:3]
		}
		warning := fmt.Sprintf("%d import(s) matched multiple Gradle dependencies; deterministic fallback selected first candidate (examples: %s)", len(s.ambiguousModules), strings.Join(examples, "; "))
		s.Warnings = append(s.Warnings, warning)
	}
}

func scanRepo(ctx context.Context, repoPath string, lookups dependencyLookups) (scanResult, error) {
	result := newScanResult()
	if repoPath == "" {
		return result, fs.ErrInvalid
	}

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		return scanKotlinAndroidSourceFile(repoPath, path, lookups, &result)
	})
	if err != nil {
		return result, err
	}

	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Kotlin/Java source files found for analysis")
	}
	result.appendInferenceWarnings()
	return result, nil
}

func scanKotlinAndroidSourceFile(repoPath string, path string, lookups dependencyLookups, result *scanResult) error {
	if !isSourceFile(path) {
		return nil
	}
	content, relativePath, err := readKotlinAndroidSource(repoPath, path)
	if err != nil {
		return err
	}
	filePackage := parsePackage(content)
	imports := parseImports(content, relativePath, filePackage, lookups, result)
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Package: filePackage,
		Imports: imports,
		Usage:   countUsage(content, imports),
	})
	return nil
}

func readKotlinAndroidSource(repoPath, path string) ([]byte, string, error) {
	if strings.TrimSpace(repoPath) == "" {
		content, err := safeio.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
		return content, path, nil
	}
	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return nil, "", err
	}
	relativePath := path
	if rel, relErr := filepath.Rel(repoPath, path); relErr == nil {
		relativePath = rel
	}
	return content, relativePath, nil
}

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt":
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

func parseImports(content []byte, filePath string, filePackage string, lookups dependencyLookups, result *scanResult) []importBinding {
	return shared.ParseImportLines(content, filePath, func(line string, _ int) []shared.ImportRecord {
		line = stripLineComment(line)
		matches := importPattern.FindStringSubmatch(line)
		if len(matches) != importPatternMatchGroups {
			return nil
		}
		module := strings.TrimSpace(matches[1])
		if module == "" {
			return nil
		}

		dependency, ambiguous := resolveDependency(module, lookups)
		if shouldIgnoreImport(module, filePackage) && dependency == "" {
			return nil
		}
		if dependency == "" {
			dependency = fallbackDependency(module)
			if dependency == "" {
				return nil
			}
			_, declared := lookups.DeclaredDependencies[normalizeDependencyID(dependency)]
			result.addFallbackModule(module, dependency, declared)
		} else if len(ambiguous) > 1 {
			result.addAmbiguousModule(module, ambiguous, dependency)
		}

		record, ok := buildImportRecord(matches, module, dependency)
		if !ok {
			return nil
		}
		return []shared.ImportRecord{record}
	})
}

func buildImportRecord(matches []string, module string, dependency string) (shared.ImportRecord, bool) {
	symbol, wildcard := resolvedImportSymbol(matches, module)
	if symbol == "" {
		return shared.ImportRecord{}, false
	}
	localName := symbol
	alias := ""
	if len(matches) > 3 {
		alias = strings.TrimSpace(matches[3])
	}
	if alias != "" && !wildcard {
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

func resolvedImportSymbol(matches []string, module string) (string, bool) {
	if len(matches) > 2 && strings.TrimSpace(matches[2]) == ".*" {
		return "*", true
	}
	return lastModuleSegment(module), false
}

func stripLineComment(line string) string {
	return shared.StripLineComment(line, "//")
}

func shouldIgnoreImport(module, filePackage string) bool {
	module = strings.TrimSpace(module)
	if module == "" {
		return true
	}

	frameworkPrefixes := []string{
		"java.", "javax.", "kotlin.", "jdk.", "sun.", "android.",
	}
	for _, prefix := range frameworkPrefixes {
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

func resolveDependency(module string, lookups dependencyLookups) (string, []string) {
	best := ""
	bestLen := 0
	bestAmbiguous := []string(nil)

	for prefix, dependency := range lookups.Prefixes {
		if module != prefix && !strings.HasPrefix(module, prefix+".") {
			continue
		}
		if len(prefix) <= bestLen {
			continue
		}
		best = dependency
		bestLen = len(prefix)
		if ambiguous, ok := lookups.Ambiguous[prefix]; ok {
			bestAmbiguous = append([]string{}, ambiguous...)
		} else {
			bestAmbiguous = nil
		}
	}
	if best != "" {
		return best, bestAmbiguous
	}

	parts := strings.Split(module, ".")
	for i := len(parts); i >= 1; i-- {
		key := strings.Join(parts[:i], ".")
		dependency, ok := lookups.Aliases[key]
		if !ok {
			continue
		}
		if ambiguous, ambiguousOK := lookups.Ambiguous[key]; ambiguousOK {
			return dependency, append([]string{}, ambiguous...)
		}
		return dependency, nil
	}

	return "", nil
}

func fallbackDependency(module string) string {
	return shared.FallbackDependency(module, normalizeDependencyID)
}

func lastModuleSegment(module string) string {
	return shared.LastModuleSegment(module)
}

func countUsage(content []byte, imports []importBinding) map[string]int {
	return shared.CountUsage(content, imports)
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, kotlinAndroidFileUsages(scan), normalizeDependencyID)
	dep := shared.BuildDependencyReportFromStats(dependency, "kotlin-android", stats)
	dep.RiskCues = kotlinAndroidRiskCues(dependency, scan, stats)
	warnings := kotlinAndroidDependencyWarnings(dependency, stats)
	dep.Recommendations = buildRecommendations(dep)
	return dep, warnings
}

func kotlinAndroidDependencyWarnings(dependency string, stats shared.DependencyStats) []string {
	if stats.HasImports {
		return nil
	}
	return []string{"no imports found for dependency " + dependency}
}

func kotlinAndroidRiskCues(dependency string, scan scanResult, stats shared.DependencyStats) []report.RiskCue {
	cues := make([]report.RiskCue, 0, 3)
	if stats.WildcardImports > 0 {
		cues = append(cues, report.RiskCue{
			Code:     "wildcard-import",
			Severity: "medium",
			Message:  "found wildcard imports for this dependency",
		})
	}
	if _, ok := scan.AmbiguousDependencies[dependency]; ok {
		cues = append(cues, report.RiskCue{
			Code:     "ambiguous-import-mapping",
			Severity: "medium",
			Message:  "some imports matched multiple Gradle dependency candidates",
		})
	}
	if _, ok := scan.UndeclaredDependencies[dependency]; ok {
		cues = append(cues, report.RiskCue{
			Code:     "undeclared-import-attribution",
			Severity: "low",
			Message:  "dependency inferred from imports but not declared in Gradle manifests",
		})
	}
	return cues
}

func buildRecommendations(dep report.DependencyReport) []report.Recommendation {
	recommendations := make([]report.Recommendation, 0, 4)
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
	if hasRiskCue(dep, "ambiguous-import-mapping") {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "review-ambiguous-gradle-mappings",
			Priority:  "medium",
			Message:   "Review imports that map to multiple Gradle coordinates and tighten declarations.",
			Rationale: "Ambiguous attribution reduces confidence in dependency removal scoring.",
		})
	}
	if hasRiskCue(dep, "undeclared-import-attribution") {
		recommendations = append(recommendations, report.Recommendation{
			Code:      "declare-missing-gradle-dependency",
			Priority:  "medium",
			Message:   "Import evidence suggests this dependency is used but not declared in Gradle manifests.",
			Rationale: "Keeping manifests aligned with imports improves build reproducibility and SBOM fidelity.",
		})
	}
	return recommendations
}

func hasRiskCue(dep report.DependencyReport, code string) bool {
	for _, cue := range dep.RiskCues {
		if cue.Code == code {
			return true
		}
	}
	return false
}

func normalizeDependencyID(value string) string {
	return shared.NormalizeDependencyID(value)
}

func shouldSkipDir(name string) bool {
	return shared.ShouldSkipDir(strings.ToLower(name), kotlinAndroidSkippedDirectories)
}

type dependencyDescriptor struct {
	Name         string
	Group        string
	Artifact     string
	Version      string
	FromManifest bool
	FromLockfile bool
}

type dependencyLookups struct {
	Prefixes             map[string]string
	Aliases              map[string]string
	Ambiguous            map[string][]string
	DeclaredDependencies map[string]struct{}
	HasLockfile          bool
}

func collectDeclaredDependencies(repoPath string) ([]dependencyDescriptor, dependencyLookups, []string) {
	manifestDescriptors := parseGradleDependencies(repoPath)
	lockfileDescriptors, hasLockfile, lockWarnings := parseGradleLockfiles(repoPath)

	descriptors := mergeDescriptors(manifestDescriptors, lockfileDescriptors)
	lookups := buildDescriptorLookups(descriptors)
	lookups.HasLockfile = hasLockfile
	return descriptors, lookups, lockWarnings
}

func mergeDescriptors(manifest, lockfile []dependencyDescriptor) []dependencyDescriptor {
	items := make(map[string]dependencyDescriptor)
	for _, descriptor := range manifest {
		key := descriptorKey(descriptor)
		descriptor.FromManifest = true
		items[key] = descriptor
	}
	for _, descriptor := range lockfile {
		key := descriptorKey(descriptor)
		descriptor.FromLockfile = true
		current, ok := items[key]
		if ok {
			current.FromLockfile = true
			if current.Version == "" {
				current.Version = descriptor.Version
			}
			items[key] = current
			continue
		}
		items[key] = descriptor
	}

	merged := make([]dependencyDescriptor, 0, len(items))
	for _, descriptor := range items {
		merged = append(merged, descriptor)
	}
	sort.Slice(merged, func(i, j int) bool {
		return compareDependencyDescriptors(merged[i], merged[j]) < 0
	})
	return merged
}

func compareDependencyDescriptors(left, right dependencyDescriptor) int {
	if cmp := strings.Compare(left.Name, right.Name); cmp != 0 {
		return cmp
	}
	if cmp := strings.Compare(left.Group, right.Group); cmp != 0 {
		return cmp
	}
	return strings.Compare(left.Artifact, right.Artifact)
}

func descriptorKey(descriptor dependencyDescriptor) string {
	if descriptor.Group == "" {
		return descriptor.Name
	}
	return descriptor.Group + ":" + descriptor.Artifact
}

func buildDescriptorLookups(descriptors []dependencyDescriptor) dependencyLookups {
	lookups := dependencyLookups{
		Prefixes:             make(map[string]string),
		Aliases:              make(map[string]string),
		Ambiguous:            make(map[string][]string),
		DeclaredDependencies: make(map[string]struct{}),
	}
	for _, descriptor := range descriptors {
		name := normalizeDependencyID(descriptor.Name)
		lookups.DeclaredDependencies[name] = struct{}{}
		addGroupLookups(lookups, name, descriptor.Group)
		addArtifactLookups(lookups, name, descriptor.Group, descriptor.Artifact)
	}
	return lookups
}

type lookupKeyStrategy func(group string, artifact string) ([]string, []string)

func addGroupLookups(lookups dependencyLookups, name string, group string) {
	addLookupByStrategy(lookups, name, group, "", groupLookupStrategy)
}

func addArtifactLookups(lookups dependencyLookups, name string, group string, artifact string) {
	addLookupByStrategy(lookups, name, group, artifact, artifactLookupStrategy)
}

func addLookupByStrategy(lookups dependencyLookups, name string, group string, artifact string, strategy lookupKeyStrategy) {
	prefixKeys, aliasKeys := strategy(group, artifact)
	for _, key := range prefixKeys {
		recordLookup(lookups.Prefixes, lookups.Ambiguous, key, name)
	}
	for _, key := range aliasKeys {
		recordLookup(lookups.Aliases, lookups.Ambiguous, key, name)
	}
}

func recordLookup(target map[string]string, ambiguous map[string][]string, key string, value string) {
	if key == "" {
		return
	}
	if existing, ok := target[key]; ok {
		if existing == value {
			return
		}
		merged := append([]string{existing, value}, ambiguous[key]...)
		ambiguous[key] = uniqueSortedStrings(merged)
		return
	}
	target[key] = value
}

func uniqueSortedStrings(values []string) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	items := make([]string, 0, len(set))
	for value := range set {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func groupLookupStrategy(group, _ string) ([]string, []string) {
	group = strings.TrimSpace(group)
	if group == "" {
		return nil, nil
	}
	aliasSet := map[string]struct{}{group: {}}
	parts := strings.Split(group, ".")
	if len(parts) >= 2 {
		aliasSet[parts[0]+"."+parts[1]] = struct{}{}
	}
	if len(parts) > 0 {
		aliasSet[parts[len(parts)-1]] = struct{}{}
	}
	aliases := make([]string, 0, len(aliasSet))
	for alias := range aliasSet {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return []string{group}, aliases
}

func artifactLookupStrategy(group, artifact string) ([]string, []string) {
	artifact = strings.ReplaceAll(strings.TrimSpace(artifact), "-", ".")
	if artifact == "" {
		return nil, nil
	}
	group = strings.TrimSpace(group)
	aliases := []string{artifact}
	if group == "" {
		return nil, aliases
	}
	return []string{group + "." + artifact}, aliases
}

var gradleCoordinatePattern = regexp.MustCompile(`(?m)\b(?:implementation|api|compileOnly|runtimeOnly|kapt|ksp|testImplementation|androidTestImplementation|testRuntimeOnly)\s*\(?\s*(?:platform\()?["']([^:"'\s]+):([^:"'\s]+)(?::([^"'\s]+))?["']\s*\)?\s*\)?`)

var gradleMapInvocationPattern = regexp.MustCompile(`(?ms)\b(?:implementation|api|compileOnly|runtimeOnly|kapt|ksp|testImplementation|androidTestImplementation|testRuntimeOnly)\s*\(?\s*((?:[A-Za-z_][A-Za-z0-9_]*\s*[:=]\s*["'][^"'\n]+["']\s*,?\s*)+)`)

var gradleNamedArgPattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*[:=]\s*["']([^"']+)["']`)

func parseGradleDependencies(repoPath string) []dependencyDescriptor {
	parser := func(content string) []dependencyDescriptor {
		return parseGradleDependencyContent(content)
	}
	return parseBuildFiles(repoPath, parser, buildGradleName, buildGradleKTSName)
}

func parseGradleDependencyContent(content string) []dependencyDescriptor {
	descriptors := make([]dependencyDescriptor, 0)
	descriptors = append(descriptors, parseGradleDependencyMatches(content, gradleCoordinatePattern)...)
	descriptors = append(descriptors, parseGradleMapDependencies(content)...)
	return dedupeDescriptors(descriptors)
}

func parseGradleMapDependencies(content string) []dependencyDescriptor {
	matches := gradleMapInvocationPattern.FindAllStringSubmatch(content, -1)
	descriptors := make([]dependencyDescriptor, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		args := match[1]
		group := ""
		artifact := ""
		version := ""
		for _, pair := range gradleNamedArgPattern.FindAllStringSubmatch(args, -1) {
			if len(pair) != 3 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(pair[1]))
			value := strings.TrimSpace(pair[2])
			switch key {
			case "group":
				group = value
			case "name":
				artifact = value
			case "version":
				version = value
			}
		}
		if group == "" || artifact == "" {
			continue
		}
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     artifact,
			Group:    group,
			Artifact: artifact,
			Version:  version,
		})
	}
	return descriptors
}

func parseGradleDependencyMatches(content string, pattern *regexp.Regexp) []dependencyDescriptor {
	matches := pattern.FindAllStringSubmatch(content, -1)
	descriptors := make([]dependencyDescriptor, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		group := strings.TrimSpace(match[1])
		artifact := strings.TrimSpace(match[2])
		version := ""
		if len(match) > 3 {
			version = strings.TrimSpace(match[3])
		}
		if group == "" || artifact == "" {
			continue
		}
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     artifact,
			Group:    group,
			Artifact: artifact,
			Version:  version,
		})
	}
	return descriptors
}

var gradleLockCoordinatePattern = regexp.MustCompile(`^\s*([^:#=\s]+):([^:#=\s]+):([^=\s]+)(?:\s*=.*)?$`)

func parseGradleLockfiles(repoPath string) ([]dependencyDescriptor, bool, []string) {
	descriptors := make([]dependencyDescriptor, 0)
	warnings := make([]string, 0)
	hasLockfile := false

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
		if strings.ToLower(entry.Name()) != gradleLockfileName {
			return nil
		}
		hasLockfile = true
		content, readErr := safeio.ReadFileUnder(repoPath, path)
		if readErr != nil {
			relPath := path
			if rel, relErr := filepath.Rel(repoPath, path); relErr == nil {
				relPath = rel
			}
			warnings = append(warnings, fmt.Sprintf("unable to read %s: %v", relPath, readErr))
			return nil
		}
		descriptors = append(descriptors, parseGradleLockfileContent(string(content))...)
		return nil
	})
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("unable to scan lockfiles: %v", err))
	}
	return dedupeDescriptors(descriptors), hasLockfile, warnings
}

func parseGradleLockfileContent(content string) []dependencyDescriptor {
	lines := strings.Split(content, "\n")
	descriptors := make([]dependencyDescriptor, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		matches := gradleLockCoordinatePattern.FindStringSubmatch(trimmed)
		if len(matches) != 4 {
			continue
		}
		group := strings.TrimSpace(matches[1])
		artifact := strings.TrimSpace(matches[2])
		version := strings.TrimSpace(matches[3])
		if group == "" || artifact == "" {
			continue
		}
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     artifact,
			Group:    group,
			Artifact: artifact,
			Version:  version,
		})
	}
	return descriptors
}

func dedupeDescriptors(descriptors []dependencyDescriptor) []dependencyDescriptor {
	if len(descriptors) == 0 {
		return nil
	}
	items := make(map[string]dependencyDescriptor)
	for _, descriptor := range descriptors {
		if descriptor.Group == "" || descriptor.Artifact == "" {
			continue
		}
		key := descriptorKey(descriptor)
		current, ok := items[key]
		if !ok {
			items[key] = descriptor
			continue
		}
		if current.Version == "" && descriptor.Version != "" {
			current.Version = descriptor.Version
		}
		items[key] = current
	}
	deduped := make([]dependencyDescriptor, 0, len(items))
	for _, descriptor := range items {
		deduped = append(deduped, descriptor)
	}
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].Name == deduped[j].Name {
			return deduped[i].Group < deduped[j].Group
		}
		return deduped[i].Name < deduped[j].Name
	})
	return deduped
}

func parseBuildFiles(repoPath string, parser func(content string) []dependencyDescriptor, names ...string) []dependencyDescriptor {
	collector := buildFileCollector{
		repoPath: repoPath,
		parser:   parser,
		names:    names,
		seen:     make(map[string]struct{}),
	}
	err := filepath.WalkDir(repoPath, collector.visit)
	if err != nil {
		return collector.descriptors
	}
	return collector.descriptors
}

type buildFileCollector struct {
	repoPath    string
	parser      func(content string) []dependencyDescriptor
	names       []string
	seen        map[string]struct{}
	descriptors []dependencyDescriptor
}

func (c *buildFileCollector) visit(path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	if !matchesBuildFile(strings.ToLower(entry.Name()), c.names) {
		return nil
	}
	content, err := safeio.ReadFileUnder(c.repoPath, path)
	if err != nil {
		return nil
	}
	for _, descriptor := range c.parser(string(content)) {
		c.recordDescriptor(descriptor)
	}
	return nil
}

func (c *buildFileCollector) recordDescriptor(descriptor dependencyDescriptor) {
	key := descriptorKey(descriptor)
	if _, ok := c.seen[key]; ok {
		return
	}
	c.seen[key] = struct{}{}
	descriptor.FromManifest = true
	c.descriptors = append(c.descriptors, descriptor)
}

func matchesBuildFile(fileName string, names []string) bool {
	for _, name := range names {
		if fileName == strings.ToLower(name) {
			return true
		}
	}
	return false
}
