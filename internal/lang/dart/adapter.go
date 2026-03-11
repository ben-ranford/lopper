package dart

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
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	Clock func() time.Time
}

const (
	pubspecYAMLName      = "pubspec.yaml"
	pubspecYMLName       = "pubspec.yml"
	pubspecLockName      = "pubspec.lock"
	maxDetectionEntries  = 2048
	maxManifestCount     = 256
	maxScanFiles         = 4096
	maxScannableDartFile = 2 * 1024 * 1024
	maxWarningSamples    = 5
)

var dartRootSignals = []shared.RootSignal{
	{Name: pubspecYAMLName, Confidence: 60},
	{Name: pubspecYMLName, Confidence: 60},
	{Name: pubspecLockName, Confidence: 20},
}

type dependencyInfo struct {
	Runtime    bool
	Dev        bool
	Override   bool
	LocalPath  bool
	FlutterSDK bool
	PluginLike bool
	Source     string
	Version    string
}

type packageManifest struct {
	Root                     string
	ManifestPath             string
	Dependencies             map[string]dependencyInfo
	HasLock                  bool
	HasFlutterSection        bool
	HasFlutterPluginMetadata bool
}

type pubspecManifest struct {
	Dependencies        map[string]any `yaml:"dependencies"`
	DevDependencies     map[string]any `yaml:"dev_dependencies"`
	DependencyOverrides map[string]any `yaml:"dependency_overrides"`
	Flutter             any            `yaml:"flutter"`
}

type pubspecLock struct {
	Packages map[string]pubspecLockPackage `yaml:"packages"`
}

type pubspecLockPackage struct {
	Dependency  string `yaml:"dependency"`
	Description any    `yaml:"description"`
	Source      string `yaml:"source"`
	Version     string `yaml:"version"`
}

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                []fileScan
	Warnings             []string
	DeclaredDependencies map[string]dependencyInfo
	UnresolvedImports    map[string]int
	HasFlutterProject    bool
	HasPluginMetadata    bool
	SkippedLargeFiles    int
	SkippedFilesByBound  bool
}

var (
	directivePattern = regexp.MustCompile(`^\s*(import|export)\s+['"]([^'"]+)['"]([^;]*);`)
	aliasPattern     = regexp.MustCompile(`\bas\s+([A-Za-z_][A-Za-z0-9_]*)`)
	identPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "dart"
}

func (a *Adapter) Aliases() []string {
	return []string{"flutter", "pub"}
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)

	detection := language.Detection{}
	roots := make(map[string]struct{})
	if err := applyDartRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return walkDartDetectionEntry(path, entry, roots, &detection, &visited)
	})
	if err != nil && err != fs.SkipAll {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func applyDartRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	return shared.ApplyRootSignals(repoPath, dartRootSignals, detection, roots)
}

func walkDartDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}

	(*visited)++
	if *visited > maxDetectionEntries {
		return fs.SkipAll
	}

	name := strings.ToLower(entry.Name())
	switch name {
	case pubspecYAMLName, pubspecYMLName:
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
	case pubspecLockName:
		detection.Matched = true
		detection.Confidence += 6
	}

	if strings.EqualFold(filepath.Ext(path), ".dart") {
		detection.Matched = true
		detection.Confidence += 2
	}
	return nil
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, result, err := a.newReport(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	scan, err := a.scanRepo(ctx, repoPath, &result)
	if err != nil {
		return report.Report{}, err
	}

	dependencies, dependencyWarnings := buildRequestedDartDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, dependencyWarnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}

func (a *Adapter) newReport(rawRepoPath string) (string, report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(rawRepoPath)
	if err != nil {
		return "", report.Report{}, err
	}

	return repoPath, report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}, nil
}

func (a *Adapter) scanRepo(ctx context.Context, repoPath string, result *report.Report) (scanResult, error) {
	manifests, warnings, err := collectManifestData(repoPath)
	if err != nil {
		return scanResult{}, err
	}
	result.Warnings = append(result.Warnings, warnings...)

	scan, err := scanRepo(ctx, repoPath, manifests)
	if err != nil {
		return scanResult{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)
	return scan, nil
}

func collectManifestData(repoPath string) ([]packageManifest, []string, error) {
	manifestPaths, warnings, err := discoverPubspecPaths(repoPath)
	if err != nil {
		return nil, nil, err
	}

	manifests := make([]packageManifest, 0, len(manifestPaths))
	for _, manifestPath := range manifestPaths {
		manifest, manifestWarnings, loadErr := loadPackageManifest(repoPath, manifestPath)
		if loadErr != nil {
			return nil, nil, loadErr
		}
		manifests = append(manifests, manifest)
		warnings = append(warnings, manifestWarnings...)
	}
	return manifests, dedupeWarnings(warnings), nil
}

func discoverPubspecPaths(repoPath string) ([]string, []string, error) {
	paths := make([]string, 0)
	warnings := make([]string, 0)
	count := 0

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !isPubspecFile(entry.Name()) {
			return nil
		}
		count++
		if count > maxManifestCount {
			return fs.SkipAll
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return nil, nil, err
	}

	if count > maxManifestCount {
		warnings = append(warnings, fmt.Sprintf("pubspec manifest discovery capped at %d files", maxManifestCount))
	}
	if len(paths) == 0 {
		warnings = append(warnings, "no pubspec.yaml or pubspec.yml files found for analysis")
	}

	return uniquePaths(paths), dedupeWarnings(warnings), nil
}

func loadPackageManifest(repoPath, manifestPath string) (packageManifest, []string, error) {
	warnings := make([]string, 0)

	manifestData, err := readPubspecManifest(repoPath, manifestPath)
	if err != nil {
		return packageManifest{}, nil, err
	}
	dependencies, hasFlutterSection, hasPluginMetadata, overrideDeps := parsePubspecDependencies(manifestData)
	root := filepath.Dir(manifestPath)

	if len(overrideDeps) > 0 {
		relManifest, relErr := filepath.Rel(repoPath, manifestPath)
		if relErr != nil {
			relManifest = manifestPath
		}
		warnings = append(warnings, fmt.Sprintf("dependency_overrides declared in %s: %s", relManifest, strings.Join(overrideDeps, ", ")))
	}

	lockPath := filepath.Join(root, pubspecLockName)
	hasLock := false
	if _, statErr := os.Stat(lockPath); statErr == nil {
		hasLock = true
		lockData, lockErr := readPubspecLock(repoPath, lockPath)
		if lockErr != nil {
			return packageManifest{}, nil, lockErr
		}
		mergeLockDependencyData(dependencies, lockData.Packages, &hasPluginMetadata)
	} else if os.IsNotExist(statErr) {
		relRoot, relErr := filepath.Rel(repoPath, root)
		if relErr != nil || relRoot == "" {
			relRoot = "."
		}
		warnings = append(warnings, fmt.Sprintf("pubspec.lock not found in %s; resolved package metadata may be partial", relRoot))
	} else {
		return packageManifest{}, nil, statErr
	}

	return packageManifest{
		Root:                     root,
		ManifestPath:             manifestPath,
		Dependencies:             dependencies,
		HasLock:                  hasLock,
		HasFlutterSection:        hasFlutterSection,
		HasFlutterPluginMetadata: hasPluginMetadata,
	}, dedupeWarnings(warnings), nil
}

func readPubspecManifest(repoPath, manifestPath string) (pubspecManifest, error) {
	return shared.ReadYAMLUnderRepo[pubspecManifest](repoPath, manifestPath)
}

func readPubspecLock(repoPath, lockPath string) (pubspecLock, error) {
	return shared.ReadYAMLUnderRepo[pubspecLock](repoPath, lockPath)
}

func parsePubspecDependencies(manifest pubspecManifest) (map[string]dependencyInfo, bool, bool, []string) {
	dependencies := make(map[string]dependencyInfo)
	hasFlutterSection := manifest.Flutter != nil
	hasPluginMetadata := hasPluginMetadataValue(manifest.Flutter)
	overrideDeps := make([]string, 0)

	addDependencySection(dependencies, manifest.Dependencies, "runtime", &hasPluginMetadata)
	addDependencySection(dependencies, manifest.DevDependencies, "dev", &hasPluginMetadata)
	addDependencySection(dependencies, manifest.DependencyOverrides, "override", &hasPluginMetadata)

	for dependency, info := range dependencies {
		if info.Override {
			overrideDeps = append(overrideDeps, dependency)
		}
	}
	sort.Strings(overrideDeps)

	return dependencies, hasFlutterSection, hasPluginMetadata, overrideDeps
}

func addDependencySection(dest map[string]dependencyInfo, section map[string]any, sectionKind string, hasPluginMetadata *bool) {
	for rawName, rawValue := range section {
		dependency := normalizeDependencyID(rawName)
		if dependency == "" {
			continue
		}
		info := dependencyInfoFromSpec(dependency, rawValue, hasPluginMetadata)
		switch sectionKind {
		case "runtime":
			info.Runtime = true
		case "dev":
			info.Dev = true
		case "override":
			info.Override = true
		}
		mergeDependencyInfo(dest, dependency, info)
	}
}

func dependencyInfoFromSpec(dependency string, value any, hasPluginMetadata *bool) dependencyInfo {
	info := dependencyInfo{
		PluginLike: isLikelyFlutterPluginPackage(dependency),
	}
	fields, ok := toStringMap(value)
	if !ok {
		return info
	}
	if asString(fields["path"]) != "" {
		info.LocalPath = true
	}
	if sdkValue := asString(fields["sdk"]); strings.EqualFold(sdkValue, "flutter") {
		info.FlutterSDK = true
	}
	if hasPluginMetadataValue(fields) {
		info.PluginLike = true
		if hasPluginMetadata != nil {
			*hasPluginMetadata = true
		}
	}
	return info
}

func mergeLockDependencyData(dest map[string]dependencyInfo, packages map[string]pubspecLockPackage, hasPluginMetadata *bool) {
	for rawName, item := range packages {
		dependency := normalizeDependencyID(rawName)
		if lockName := lockPackageName(item.Description); lockName != "" {
			dependency = lockName
		}
		if dependency == "" {
			continue
		}
		classification := strings.ToLower(strings.TrimSpace(item.Dependency))
		info := dependencyInfo{
			Runtime:    strings.Contains(classification, "main"),
			Dev:        strings.Contains(classification, "dev"),
			Override:   strings.Contains(classification, "overrid"),
			FlutterSDK: strings.EqualFold(strings.TrimSpace(item.Source), "sdk") && lockDescriptionTargetsFlutter(item.Description),
			LocalPath:  strings.EqualFold(strings.TrimSpace(item.Source), "path"),
			PluginLike: isLikelyFlutterPluginPackage(dependency),
			Source:     strings.TrimSpace(item.Source),
			Version:    strings.TrimSpace(item.Version),
		}
		if hasPluginMetadataValue(item.Description) {
			info.PluginLike = true
			if hasPluginMetadata != nil {
				*hasPluginMetadata = true
			}
		}
		mergeDependencyInfo(dest, dependency, info)
	}
}

func lockPackageName(description any) string {
	fields, ok := toStringMap(description)
	if !ok {
		return ""
	}
	return normalizeDependencyID(asString(fields["name"]))
}

func lockDescriptionTargetsFlutter(description any) bool {
	if strings.EqualFold(strings.TrimSpace(asString(description)), "flutter") {
		return true
	}
	fields, ok := toStringMap(description)
	if !ok {
		return false
	}
	if strings.EqualFold(asString(fields["name"]), "flutter") {
		return true
	}
	if strings.EqualFold(asString(fields["sdk"]), "flutter") {
		return true
	}
	return false
}

func mergeDependencyInfo(dest map[string]dependencyInfo, dependency string, incoming dependencyInfo) {
	current, ok := dest[dependency]
	if !ok {
		dest[dependency] = incoming
		return
	}
	current.Runtime = current.Runtime || incoming.Runtime
	current.Dev = current.Dev || incoming.Dev
	current.Override = current.Override || incoming.Override
	current.LocalPath = current.LocalPath || incoming.LocalPath
	current.FlutterSDK = current.FlutterSDK || incoming.FlutterSDK
	current.PluginLike = current.PluginLike || incoming.PluginLike
	if current.Source == "" {
		current.Source = incoming.Source
	}
	if current.Version == "" {
		current.Version = incoming.Version
	}
	dest[dependency] = current
}

func scanRepo(ctx context.Context, repoPath string, manifests []packageManifest) (scanResult, error) {
	result := scanResult{
		DeclaredDependencies: make(map[string]dependencyInfo),
		UnresolvedImports:    make(map[string]int),
	}

	if len(manifests) == 0 {
		manifests = []packageManifest{{
			Root:         repoPath,
			Dependencies: map[string]dependencyInfo{},
		}}
	}

	allRoots := collectManifestRoots(manifests)
	scannedFiles := make(map[string]struct{})
	fileCount := 0

	for _, manifest := range manifests {
		mergeDeclaredDependencies(result.DeclaredDependencies, manifest.Dependencies)
		result.HasFlutterProject = result.HasFlutterProject || manifest.HasFlutterSection
		result.HasPluginMetadata = result.HasPluginMetadata || manifest.HasFlutterPluginMetadata

		err := scanPackageRoot(ctx, repoPath, manifest, allRoots, scannedFiles, &fileCount, &result)
		if err != nil && err != fs.SkipAll {
			return scanResult{}, err
		}
	}

	if result.HasFlutterProject && !result.HasPluginMetadata {
		result.Warnings = append(result.Warnings, "flutter plugin metadata not found in local manifests; plugin classification uses conservative heuristics")
	}
	result.Warnings = append(result.Warnings, compileScanWarnings(result)...)
	result.Warnings = dedupeWarnings(result.Warnings)
	return result, nil
}

func collectManifestRoots(manifests []packageManifest) map[string]struct{} {
	roots := make(map[string]struct{}, len(manifests))
	for _, manifest := range manifests {
		root := filepath.Clean(strings.TrimSpace(manifest.Root))
		if root == "" {
			continue
		}
		roots[root] = struct{}{}
	}
	return roots
}

func mergeDeclaredDependencies(dest, incoming map[string]dependencyInfo) {
	for dependency, info := range incoming {
		mergeDependencyInfo(dest, dependency, info)
	}
}

func scanPackageRoot(ctx context.Context, repoPath string, manifest packageManifest, allRoots map[string]struct{}, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	root := manifest.Root
	if root == "" {
		root = repoPath
	}

	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		return walkPackageEntry(ctx, root, repoPath, path, entry, walkErr, manifest.Dependencies, allRoots, scannedFiles, fileCount, result)
	})
}

func walkPackageEntry(ctx context.Context, root, repoPath, path string, entry fs.DirEntry, walkErr error, depLookup map[string]dependencyInfo, allRoots map[string]struct{}, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	if err := walkContextErr(ctx, walkErr); err != nil {
		return err
	}
	if entry.IsDir() {
		return scanPackageDir(root, path, entry.Name(), allRoots)
	}
	return scanPackageFileEntry(repoPath, path, depLookup, scannedFiles, fileCount, result)
}

func walkContextErr(ctx context.Context, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func scanPackageDir(root, path, name string, allRoots map[string]struct{}) error {
	if shouldSkipDir(name) {
		return filepath.SkipDir
	}
	if path == root {
		return nil
	}
	if _, ok := allRoots[filepath.Clean(path)]; ok {
		return filepath.SkipDir
	}
	return nil
}

func scanPackageFileEntry(repoPath string, path string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	if !strings.EqualFold(filepath.Ext(path), ".dart") {
		return nil
	}
	cleanPath := filepath.Clean(path)
	if _, ok := scannedFiles[cleanPath]; ok {
		return nil
	}
	scannedFiles[cleanPath] = struct{}{}

	(*fileCount)++
	if *fileCount > maxScanFiles {
		result.SkippedFilesByBound = true
		return fs.SkipAll
	}
	return scanDartSourceFile(repoPath, cleanPath, depLookup, result)
}

func scanDartSourceFile(repoPath, path string, depLookup map[string]dependencyInfo, result *scanResult) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > maxScannableDartFile {
		result.SkippedLargeFiles++
		return nil
	}

	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return err
	}
	relativePath, relErr := filepath.Rel(repoPath, path)
	if relErr != nil {
		relativePath = path
	}
	imports := parseDartImports(content, relativePath, depLookup, result.UnresolvedImports)
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Imports: imports,
		Usage:   shared.CountUsage(content, imports),
	})
	return nil
}

func parseDartImports(content []byte, filePath string, depLookup map[string]dependencyInfo, unresolved map[string]int) []importBinding {
	lines := strings.Split(string(content), "\n")
	imports := make([]importBinding, 0)
	for i, line := range lines {
		kind, module, clause, ok := parseImportDirective(line)
		if !ok {
			continue
		}
		dependency := resolveDependencyFromModule(module, depLookup, unresolved)
		if dependency == "" {
			continue
		}
		location := report.Location{
			File:   filePath,
			Line:   i + 1,
			Column: shared.FirstContentColumn(line),
		}
		imports = append(imports, buildDirectiveBindings(kind, module, clause, dependency, location)...)
	}
	return imports
}

func parseImportDirective(line string) (string, string, string, bool) {
	match := directivePattern.FindStringSubmatch(line)
	if len(match) != 4 {
		return "", "", "", false
	}
	kind := strings.TrimSpace(strings.ToLower(match[1]))
	module := strings.TrimSpace(match[2])
	clause := strings.TrimSpace(match[3])
	if kind != "import" && kind != "export" {
		return "", "", "", false
	}
	return kind, module, clause, true
}

func buildDirectiveBindings(kind, module, clause, dependency string, location report.Location) []importBinding {
	if kind == "export" {
		return []importBinding{{
			Dependency: dependency,
			Module:     module,
			Name:       "*",
			Local:      dependency,
			Wildcard:   true,
			Location:   location,
		}}
	}

	alias := extractAlias(clause)
	showSymbols := parseShowSymbols(clause)
	bindings := make([]importBinding, 0, 1+len(showSymbols))

	if alias != "" {
		bindings = append(bindings, importBinding{
			Dependency: dependency,
			Module:     module,
			Name:       alias,
			Local:      alias,
			Location:   location,
		})
	}

	if alias == "" && len(showSymbols) > 0 {
		for _, symbol := range showSymbols {
			bindings = append(bindings, importBinding{
				Dependency: dependency,
				Module:     module,
				Name:       symbol,
				Local:      symbol,
				Location:   location,
			})
		}
	}

	if len(bindings) > 0 {
		return bindings
	}
	return []importBinding{{
		Dependency: dependency,
		Module:     module,
		Name:       "*",
		Local:      dependency,
		Wildcard:   true,
		Location:   location,
	}}
}

func extractAlias(clause string) string {
	match := aliasPattern.FindStringSubmatch(clause)
	if len(match) != 2 {
		return ""
	}
	alias := strings.TrimSpace(match[1])
	if !identPattern.MatchString(alias) {
		return ""
	}
	return alias
}

func parseShowSymbols(clause string) []string {
	lowerClause := strings.ToLower(clause)
	showIndex := strings.Index(lowerClause, "show ")
	if showIndex < 0 {
		return nil
	}
	list := strings.TrimSpace(clause[showIndex+len("show "):])
	if list == "" {
		return nil
	}
	if hideIndex := strings.Index(strings.ToLower(list), " hide "); hideIndex >= 0 {
		list = strings.TrimSpace(list[:hideIndex])
	}
	if list == "" {
		return nil
	}
	items := make([]string, 0)
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if !identPattern.MatchString(part) {
			continue
		}
		items = append(items, part)
	}
	return dedupeStrings(items)
}

func resolveDependencyFromModule(module string, depLookup map[string]dependencyInfo, unresolved map[string]int) string {
	module = strings.TrimSpace(module)
	if !strings.HasPrefix(module, "package:") {
		return ""
	}
	remainder := strings.TrimPrefix(module, "package:")
	if remainder == "" {
		return ""
	}
	dependency := remainder
	if slash := strings.Index(dependency, "/"); slash >= 0 {
		dependency = dependency[:slash]
	}
	dependency = normalizeDependencyID(dependency)
	if dependency == "" {
		return ""
	}
	if info, ok := depLookup[dependency]; ok {
		if info.LocalPath {
			return ""
		}
		return dependency
	}
	if unresolved != nil {
		unresolved[dependency]++
	}
	return dependency
}

func compileScanWarnings(result scanResult) []string {
	warnings := make([]string, 0, 4+len(result.UnresolvedImports))
	if len(result.Files) == 0 {
		warnings = append(warnings, "no Dart source files found for analysis")
	}
	if result.SkippedLargeFiles > 0 {
		warnings = append(warnings, fmt.Sprintf("skipped %d Dart files larger than %d bytes", result.SkippedLargeFiles, maxScannableDartFile))
	}
	if result.SkippedFilesByBound {
		warnings = append(warnings, fmt.Sprintf("Dart source scanning capped at %d files", maxScanFiles))
	}
	return append(warnings, summarizeUnresolved(result.UnresolvedImports)...)
}

func buildRequestedDartDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	minUsageThreshold := resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations)
	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		depReport, warnings := buildDependencyReport(dependency, scan, minUsageThreshold)
		return []report.DependencyReport{depReport}, warnings
	case req.TopN > 0:
		return buildTopDartDependencies(req.TopN, scan, minUsageThreshold)
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func buildTopDartDependencies(topN int, scan scanResult, minUsageThreshold int) ([]report.DependencyReport, []string) {
	dependencies := allDependencies(scan)
	return shared.BuildTopReports(topN, dependencies, func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan, minUsageThreshold)
	})
}

func allDependencies(scan scanResult) []string {
	set := make(map[string]struct{})
	for dependency, info := range scan.DeclaredDependencies {
		if info.LocalPath {
			continue
		}
		set[dependency] = struct{}{}
	}

	fileUsages := dartFileUsages(scan)
	for _, dependency := range shared.ListDependencies(fileUsages, normalizeDependencyID) {
		set[dependency] = struct{}{}
	}
	return shared.SortedKeys(set)
}

func buildDependencyReport(dependency string, scan scanResult, minUsageThreshold int) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, dartFileUsages(scan), normalizeDependencyID)
	meta, declared := scan.DeclaredDependencies[dependency]

	dep := report.DependencyReport{
		Language:             "dart",
		Name:                 dependency,
		UsedExportsCount:     stats.UsedCount,
		TotalExportsCount:    stats.TotalCount,
		UsedPercent:          stats.UsedPercent,
		EstimatedUnusedBytes: 0,
		TopUsedSymbols:       stats.TopSymbols,
		UsedImports:          stats.UsedImports,
		UnusedImports:        stats.UnusedImports,
	}

	warnings := make([]string, 0, 1)
	if !stats.HasImports {
		warnings = append(warnings, fmt.Sprintf("no imports found for dependency %q", dependency))
	}

	if !declared && stats.HasImports {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "undeclared-package-import",
			Severity: "medium",
			Message:  "package import was detected but the dependency is not declared in pubspec",
		})
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "declare-missing-dependency",
			Priority:  "high",
			Message:   fmt.Sprintf("Add %q to pubspec dependencies or remove the import.", dependency),
			Rationale: "Undeclared dependencies can break reproducible builds and reviews.",
		})
	}
	if declared {
		if meta.Override {
			dep.RiskCues = append(dep.RiskCues, report.RiskCue{
				Code:     "dependency-override",
				Severity: "medium",
				Message:  "dependency is marked in dependency_overrides",
			})
			dep.Recommendations = append(dep.Recommendations, report.Recommendation{
				Code:      "review-dependency-override",
				Priority:  "medium",
				Message:   "Review dependency_overrides usage and limit overrides to active blockers.",
				Rationale: "Overrides can hide upstream changes and create drift over time.",
			})
		}
		if meta.FlutterSDK {
			dep.RiskCues = append(dep.RiskCues, report.RiskCue{
				Code:     "flutter-sdk-dependency",
				Severity: "low",
				Message:  "dependency is provided by the Flutter SDK",
			})
		}
		if meta.PluginLike {
			dep.RiskCues = append(dep.RiskCues, report.RiskCue{
				Code:     "flutter-plugin-dependency",
				Severity: "medium",
				Message:  "dependency appears to be a Flutter plugin package with platform bindings",
			})
			dep.Recommendations = append(dep.Recommendations, report.Recommendation{
				Code:      "audit-plugin-removal",
				Priority:  "medium",
				Message:   "Audit native platform impact before removing this Flutter plugin dependency.",
				Rationale: "Plugin dependencies can bind Android/iOS platform code beyond Dart call sites.",
			})
		}
	}
	if stats.WildcardImports > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "broad-imports",
			Severity: "low",
			Message:  "broad import/export directives may reduce static attribution precision",
		})
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "prefer-explicit-imports",
			Priority:  "low",
			Message:   "Prefer explicit imports (`show`) or prefixes (`as`) for tighter attribution.",
			Rationale: "Explicit import surfaces improve confidence in static usage analysis.",
		})
	}
	if dep.TotalExportsCount > 0 && dep.UsedPercent > 0 && dep.UsedPercent < float64(minUsageThreshold) {
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "reduce-dart-surface-area",
			Priority:  "low",
			Message:   fmt.Sprintf("Only %.1f%% of %q imports appear used; review if a leaner package would suffice.", dep.UsedPercent, dependency),
			Rationale: "Low observed usage can indicate avoidable dependency surface area.",
		})
	}
	if declared && stats.TotalCount == 0 {
		dep.Recommendations = append(dep.Recommendations, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   "No imports were detected for this declared dependency; consider removing it.",
			Rationale: "Unused dependencies increase attack and maintenance surface.",
		})
	}

	sort.Slice(dep.RiskCues, func(i, j int) bool {
		return dep.RiskCues[i].Code < dep.RiskCues[j].Code
	})
	shared.SortRecommendations(dep.Recommendations, recommendationPriorityRank)

	return dep, warnings
}

func dartFileUsages(scan scanResult) []shared.FileUsage {
	imports := func(file fileScan) []shared.ImportRecord { return file.Imports }
	usage := func(file fileScan) map[string]int { return file.Usage }
	return shared.MapFileUsages(scan.Files, imports, usage)
}

func resolveMinUsageRecommendationThreshold(value *int) int {
	if value != nil {
		return *value
	}
	return thresholds.Defaults().MinUsagePercentForRecommendations
}

func summarizeUnresolved(unresolved map[string]int) []string {
	dependencies := shared.TopCountKeys(unresolved, maxWarningSamples)
	warnings := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		warnings = append(warnings, fmt.Sprintf("could not resolve Dart package import %q from pubspec data", dependency))
	}
	return warnings
}

func recommendationPriorityRank(priority string) int {
	switch priority {
	case "high":
		return 0
	case "medium":
		return 1
	default:
		return 2
	}
}

func isPubspecFile(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == pubspecYAMLName || name == pubspecYMLName
}

func normalizeDependencyID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return value
}

func shouldSkipDir(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", ".idea", ".vscode", ".dart_tool", ".artifacts", "build", "dist", "vendor", "node_modules", "pods", ".gradle", "android", "ios", "macos", "linux", "windows":
		return true
	default:
		return false
	}
}

func hasPluginMetadataValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		return hasPluginMetadataStringMap(typed)
	case map[any]any:
		return hasPluginMetadataAnyMap(typed)
	case []any:
		return hasPluginMetadataSlice(typed)
	}
	return false
}

func hasPluginMetadataStringMap(values map[string]any) bool {
	for key, nested := range values {
		if isPluginMetadataKey(key) || hasPluginMetadataValue(nested) {
			return true
		}
	}
	return false
}

func hasPluginMetadataAnyMap(values map[any]any) bool {
	for key, nested := range values {
		if isPluginMetadataKey(fmt.Sprint(key)) || hasPluginMetadataValue(nested) {
			return true
		}
	}
	return false
}

func hasPluginMetadataSlice(values []any) bool {
	for _, item := range values {
		if hasPluginMetadataValue(item) {
			return true
		}
	}
	return false
}

func isPluginMetadataKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "plugin", "pluginclass", "ffiplugin", "platforms":
		return true
	default:
		return false
	}
}

func isLikelyFlutterPluginPackage(dependency string) bool {
	dependency = normalizeDependencyID(dependency)
	switch {
	case strings.HasSuffix(dependency, "_android"),
		strings.HasSuffix(dependency, "_ios"),
		strings.HasSuffix(dependency, "_macos"),
		strings.HasSuffix(dependency, "_linux"),
		strings.HasSuffix(dependency, "_windows"),
		strings.HasSuffix(dependency, "_web"),
		strings.Contains(dependency, "_platform_interface"):
		return true
	default:
		return false
	}
}

func toStringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[fmt.Sprint(key)] = item
		}
		return converted, true
	default:
		return nil, false
	}
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func uniquePaths(values []string) []string {
	return shared.UniqueCleanPaths(values)
}

func dedupeWarnings(warnings []string) []string {
	return shared.UniqueTrimmedStrings(warnings)
}

func dedupeStrings(values []string) []string {
	return shared.UniqueTrimmedStrings(values)
}
