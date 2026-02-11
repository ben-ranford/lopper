package golang

import (
	"context"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	Clock func() time.Time
}

const (
	goModName            = "go.mod"
	goWorkName           = "go.work"
	maxScannableGoFile   = 2 * 1024 * 1024
	maxGoBuildHeaderLine = 64
)

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "go"
}

func (a *Adapter) Aliases() []string {
	return []string{"golang"}
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
	if err := applyGoRootSignals(repoPath, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	const maxFiles = 1024
	visited := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return walkGoDetectionEntry(path, entry, roots, &detection, &visited, maxFiles)
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
	detection.Roots = shared.SortedKeys(roots)
	return detection, nil
}

func walkGoDetectionEntry(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection, visited *int, maxFiles int) error {
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
	updateGoDetection(path, entry, roots, detection)
	return nil
}

func applyGoRootSignals(repoPath string, detection *language.Detection, roots map[string]struct{}) error {
	rootSignals := []struct {
		name       string
		confidence int
	}{
		{name: goModName, confidence: 55},
		{name: goWorkName, confidence: 45},
	}
	for _, signal := range rootSignals {
		candidate := filepath.Join(repoPath, signal.name)
		if _, err := os.Stat(candidate); err == nil {
			detection.Matched = true
			detection.Confidence += signal.confidence
			roots[repoPath] = struct{}{}
			if signal.name == goWorkName {
				if err := addGoWorkRoots(repoPath, roots); err != nil {
					return err
				}
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func addGoWorkRoots(repoPath string, roots map[string]struct{}) error {
	workPath := filepath.Join(repoPath, goWorkName)
	// #nosec G304 -- path is constrained under normalized repoPath.
	content, err := os.ReadFile(workPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, rel := range parseGoWorkUseEntries(content) {
		if rel == "" {
			continue
		}
		roots[filepath.Clean(filepath.Join(repoPath, rel))] = struct{}{}
	}
	return nil
}

func updateGoDetection(path string, entry fs.DirEntry, roots map[string]struct{}, detection *language.Detection) {
	switch strings.ToLower(entry.Name()) {
	case goModName, goWorkName:
		detection.Matched = true
		detection.Confidence += 12
		roots[filepath.Dir(path)] = struct{}{}
	}
	if strings.EqualFold(filepath.Ext(path), ".go") {
		detection.Matched = true
		detection.Confidence += 2
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

	moduleInfo, err := loadGoModuleInfo(repoPath)
	if err != nil {
		return report.Report{}, err
	}

	scanResult, err := scanRepo(ctx, repoPath, moduleInfo)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	dependencies, warnings := buildRequestedGoDependencies(req, scanResult)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	return result, nil
}

func buildRequestedGoDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	switch {
	case req.Dependency != "":
		dependency := normalizeDependencyID(req.Dependency)
		depReport, warnings := buildDependencyReport(dependency, scan)
		return []report.DependencyReport{depReport}, warnings
	case req.TopN > 0:
		return buildTopGoDependencies(req.TopN, scan)
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func buildTopGoDependencies(topN int, scan scanResult) ([]report.DependencyReport, []string) {
	fileUsages := shared.MapFileUsages(
		scan.Files,
		func(file fileScan) []shared.ImportRecord { return file.Imports },
		func(file fileScan) map[string]int { return file.Usage },
	)
	dependencies := shared.ListDependencies(fileUsages, normalizeDependencyID)
	return shared.BuildTopReports(topN, dependencies, func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	})
}

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                         []fileScan
	Warnings                      []string
	BlankImportsByDependency      map[string]int
	UndeclaredImportsByDependency map[string]int
	SkippedGeneratedFiles         int
	SkippedBuildTaggedFiles       int
	SkippedLargeFiles             int
	SkippedNestedModuleDirs       int
}

type moduleInfo struct {
	ModulePath           string
	LocalModulePaths     []string
	DeclaredDependencies []string
	ReplacementImports   map[string]string
}

func scanRepo(ctx context.Context, repoPath string, moduleInfo moduleInfo) (scanResult, error) {
	result := newScanResult()
	if repoPath == "" {
		return result, fs.ErrInvalid
	}
	nestedModules, err := nestedModuleDirs(repoPath)
	if err != nil {
		return result, err
	}

	err = walkGoFiles(ctx, repoPath, nestedModules, moduleInfo, &result)
	if err != nil {
		return result, err
	}
	appendScanWarnings(&result, moduleInfo)
	return result, nil
}

func newScanResult() scanResult {
	return scanResult{
		BlankImportsByDependency:      make(map[string]int),
		UndeclaredImportsByDependency: make(map[string]int),
	}
}

func walkGoFiles(ctx context.Context, repoPath string, nestedModules map[string]struct{}, moduleInfo moduleInfo, result *scanResult) error {
	return filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			return handleScanDirEntry(path, repoPath, entry, nestedModules, result)
		}
		if !strings.EqualFold(filepath.Ext(path), ".go") {
			return nil
		}
		return scanGoSourceFile(repoPath, path, moduleInfo, result)
	})
}

func handleScanDirEntry(path, repoPath string, entry fs.DirEntry, nestedModules map[string]struct{}, result *scanResult) error {
	if shouldSkipDir(entry.Name()) {
		return filepath.SkipDir
	}
	if path != repoPath {
		if _, ok := nestedModules[path]; ok {
			if result != nil {
				result.SkippedNestedModuleDirs++
			}
			return filepath.SkipDir
		}
	}
	return nil
}

func appendScanWarnings(result *scanResult, moduleInfo moduleInfo) {
	if result == nil {
		return
	}
	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Go source files found for analysis")
	}
	if len(moduleInfo.DeclaredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no Go dependencies discovered from go.mod")
	}
	appendSkipWarnings(result)
	appendUndeclaredDependencyWarnings(result)
}

func appendSkipWarnings(result *scanResult) {
	if result == nil {
		return
	}
	if result.SkippedGeneratedFiles > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d generated Go file(s)", result.SkippedGeneratedFiles))
	}
	if result.SkippedBuildTaggedFiles > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d Go file(s) due to build constraints", result.SkippedBuildTaggedFiles))
	}
	if result.SkippedLargeFiles > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d large Go file(s) above %d bytes", result.SkippedLargeFiles, maxScannableGoFile))
	}
	if result.SkippedNestedModuleDirs > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d nested module directorie(s) while scanning root module", result.SkippedNestedModuleDirs))
	}
}

func appendUndeclaredDependencyWarnings(result *scanResult) {
	if result == nil {
		return
	}
	for dependency, count := range result.UndeclaredImportsByDependency {
		result.Warnings = append(
			result.Warnings,
			fmt.Sprintf("found %d import(s) mapped to %q that are not declared in go.mod", count, dependency),
		)
	}
}

func scanGoSourceFile(repoPath, path string, moduleInfo moduleInfo, result *scanResult) error {
	// #nosec G304 -- path originates from filepath.WalkDir rooted at repoPath.
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(content) > maxScannableGoFile {
		if result != nil {
			result.SkippedLargeFiles++
		}
		return nil
	}
	if isGeneratedGoFile(content) {
		if result != nil {
			result.SkippedGeneratedFiles++
		}
		return nil
	}
	if !matchesActiveBuild(content) {
		if result != nil {
			result.SkippedBuildTaggedFiles++
		}
		return nil
	}
	relativePath, err := filepath.Rel(repoPath, path)
	if err != nil {
		relativePath = path
	}

	imports, metadata := parseImports(content, relativePath, moduleInfo)
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Imports: imports,
		Usage:   shared.CountUsage(content, imports),
	})
	applyImportMetadata(metadata, result)
	return nil
}

func nestedModuleDirs(repoPath string) (map[string]struct{}, error) {
	dirs := make(map[string]struct{})
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		if path == repoPath {
			return nil
		}
		if _, err := os.Stat(filepath.Join(path, goModName)); err == nil {
			dirs[path] = struct{}{}
			return filepath.SkipDir
		} else if !os.IsNotExist(err) {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

func discoverNestedModules(repoPath string) ([]string, []string, map[string]string, error) {
	nestedDirs, err := nestedModuleDirs(repoPath)
	if err != nil {
		return nil, nil, nil, err
	}

	modules := make([]string, 0, len(nestedDirs))
	dependencies := make([]string, 0)
	replacements := make(map[string]string)
	for dir := range nestedDirs {
		modulePath, deps, moduleReplacements, err := loadGoModFromDir(dir)
		if err != nil {
			continue
		}
		if modulePath != "" {
			modules = append(modules, modulePath)
		}
		dependencies = append(dependencies, deps...)
		for replacementImport, dependency := range moduleReplacements {
			if _, ok := replacements[replacementImport]; !ok {
				replacements[replacementImport] = dependency
			}
		}
	}

	return uniqueStrings(modules), uniqueStrings(dependencies), replacements, nil
}

type importMetadata struct {
	Dependency string
	IsBlank    bool
	Undeclared bool
}

func parseImports(content []byte, relativePath string, moduleInfo moduleInfo) ([]importBinding, []importMetadata) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, relativePath, content, parser.ImportsOnly)
	if err != nil {
		return nil, nil
	}

	bindings := make([]importBinding, 0, len(parsed.Imports))
	metadata := make([]importMetadata, 0, len(parsed.Imports))
	for _, imported := range parsed.Imports {
		importPath := trimImportPath(imported)
		if importPath == "" {
			continue
		}

		dependency := dependencyFromImport(importPath, moduleInfo)
		if dependency == "" {
			continue
		}

		name, local, wildcard := importBindingIdentity(importPath, imported.Name)
		position := fileSet.Position(imported.Pos())
		bindings = append(bindings, importBinding{
			Dependency: dependency,
			Module:     importPath,
			Name:       name,
			Local:      local,
			Wildcard:   wildcard,
			Location: report.Location{
				File:   relativePath,
				Line:   position.Line,
				Column: position.Column,
			},
		})
		metadata = append(metadata, importMetadata{
			Dependency: dependency,
			IsBlank:    imported.Name != nil && imported.Name.Name == "_",
			Undeclared: !isDeclaredDependency(dependency, moduleInfo.DeclaredDependencies),
		})
	}

	return bindings, metadata
}

func trimImportPath(imported *ast.ImportSpec) string {
	if imported == nil || imported.Path == nil {
		return ""
	}
	return strings.Trim(imported.Path.Value, "\"")
}

func isGeneratedGoFile(content []byte) bool {
	lines := strings.Split(string(content), "\n")
	maxLines := minInt(len(lines), 20)
	for i := 0; i < maxLines; i++ {
		line := strings.ToLower(strings.TrimSpace(lines[i]))
		if strings.Contains(line, "code generated") && strings.Contains(line, "do not edit") {
			return true
		}
	}
	return false
}

func matchesActiveBuild(content []byte) bool {
	goBuildExpr, plusBuildExprs := extractBuildConstraintExpressions(content)
	switch {
	case goBuildExpr != nil:
		return goBuildExpr.Eval(isActiveBuildTag)
	case len(plusBuildExprs) > 0:
		for _, expr := range plusBuildExprs {
			if !expr.Eval(isActiveBuildTag) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func extractBuildConstraintExpressions(content []byte) (constraint.Expr, []constraint.Expr) {
	lines := strings.Split(string(content), "\n")
	maxLines := minInt(len(lines), maxGoBuildHeaderLine)
	plusBuildExprs := make([]constraint.Expr, 0)
	var goBuildExpr constraint.Expr

	for i := 0; i < maxLines; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if shouldStopBuildConstraintScan(line) {
			break
		}
		expr, kind := parseBuildConstraintComment(line)
		switch kind {
		case "go":
			if expr != nil {
				goBuildExpr = expr
			}
		case "plus":
			if expr != nil {
				plusBuildExprs = append(plusBuildExprs, expr)
			}
		}
	}
	return goBuildExpr, plusBuildExprs
}

func shouldStopBuildConstraintScan(line string) bool {
	if strings.HasPrefix(line, "package ") {
		return true
	}
	return !strings.HasPrefix(line, "//")
}

func parseBuildConstraintComment(line string) (constraint.Expr, string) {
	switch {
	case strings.HasPrefix(line, "//go:build "):
		expr, err := constraint.Parse(line)
		if err != nil {
			return nil, "go"
		}
		return expr, "go"
	case strings.HasPrefix(line, "// +build "):
		expr, err := constraint.Parse(line)
		if err != nil {
			return nil, "plus"
		}
		return expr, "plus"
	default:
		return nil, ""
	}
}

func isActiveBuildTag(tag string) bool {
	tag = strings.TrimSpace(strings.ToLower(tag))
	if tag == "" {
		return false
	}
	if tag == runtime.GOOS || tag == runtime.GOARCH {
		return true
	}
	if tag == "unix" {
		switch runtime.GOOS {
		case "android", "darwin", "dragonfly", "freebsd", "illumos", "ios", "linux", "netbsd", "openbsd", "solaris":
			return true
		}
	}
	if tag == "cgo" {
		return strings.EqualFold(os.Getenv("CGO_ENABLED"), "1")
	}
	if strings.HasPrefix(tag, "go1.") {
		return isSupportedGoReleaseTag(tag)
	}
	// Unknown tags are treated as disabled unless set explicitly.
	return false
}

func isSupportedGoReleaseTag(tag string) bool {
	current := strings.TrimPrefix(runtime.Version(), "go")
	current = strings.TrimPrefix(current, "devel ")
	if current == runtime.Version() {
		// Non-standard runtime string.
		return false
	}
	current = strings.SplitN(current, " ", 2)[0]
	current = strings.SplitN(current, "-", 2)[0]
	currentParts := strings.Split(current, ".")
	if len(currentParts) < 2 {
		return false
	}
	minorCurrent := parseIntDefault(currentParts[1], 0)
	tagParts := strings.Split(strings.TrimPrefix(tag, "go1."), ".")
	if len(tagParts) == 0 {
		return false
	}
	minorTag := parseIntDefault(tagParts[0], 0)
	return minorTag <= minorCurrent
}

func parseIntDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	n := 0
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return fallback
		}
		n = (n * 10) + int(value[i]-'0')
	}
	return n
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func applyImportMetadata(metadata []importMetadata, result *scanResult) {
	if result == nil {
		return
	}
	for _, item := range metadata {
		if item.Dependency == "" {
			continue
		}
		if item.IsBlank {
			result.BlankImportsByDependency[item.Dependency]++
		}
		if item.Undeclared {
			result.UndeclaredImportsByDependency[item.Dependency]++
		}
	}
}

func dependencyFromImport(importPath string, moduleInfo moduleInfo) string {
	importPath = strings.TrimSpace(importPath)
	if importPath == "" || importPath == "C" {
		return ""
	}
	if isLocalModuleImport(importPath, moduleInfo.LocalModulePaths) {
		return ""
	}
	if !looksExternalImport(importPath) {
		return ""
	}
	if dependency := longestDeclaredDependency(importPath, moduleInfo.DeclaredDependencies); dependency != "" {
		return normalizeDependencyID(dependency)
	}
	if dependency := longestReplacementDependency(importPath, moduleInfo.ReplacementImports); dependency != "" {
		return normalizeDependencyID(dependency)
	}
	return normalizeDependencyID(inferDependency(importPath))
}

func isLocalModuleImport(importPath string, localModules []string) bool {
	for _, modulePath := range localModules {
		if modulePath == "" {
			continue
		}
		if importPath == modulePath || strings.HasPrefix(importPath, modulePath+"/") {
			return true
		}
	}
	return false
}

func looksExternalImport(importPath string) bool {
	parts := strings.Split(importPath, "/")
	if len(parts) == 0 {
		return false
	}
	return strings.Contains(parts[0], ".")
}

func longestDeclaredDependency(importPath string, declaredDependencies []string) string {
	match := ""
	for _, dependency := range declaredDependencies {
		if importPath != dependency && !strings.HasPrefix(importPath, dependency+"/") {
			continue
		}
		if len(dependency) > len(match) {
			match = dependency
		}
	}
	return match
}

func longestReplacementDependency(importPath string, replacements map[string]string) string {
	if len(replacements) == 0 {
		return ""
	}
	match := ""
	for replacementImport := range replacements {
		if importPath != replacementImport && !strings.HasPrefix(importPath, replacementImport+"/") {
			continue
		}
		if len(replacementImport) > len(match) {
			match = replacementImport
		}
	}
	if match == "" {
		return ""
	}
	return replacements[match]
}

func inferDependency(importPath string) string {
	parts := strings.Split(importPath, "/")
	if len(parts) == 0 {
		return ""
	}
	if !strings.Contains(parts[0], ".") {
		return ""
	}
	if len(parts) >= 3 {
		return strings.Join(parts[:3], "/")
	}
	return strings.Join(parts, "/")
}

func isDeclaredDependency(dependency string, declaredDependencies []string) bool {
	for _, declared := range declaredDependencies {
		if normalizeDependencyID(declared) == normalizeDependencyID(dependency) {
			return true
		}
	}
	return false
}

func importBindingIdentity(importPath string, importName *ast.Ident) (string, string, bool) {
	base := path.Base(importPath)
	if importName == nil {
		return base, base, false
	}
	switch importName.Name {
	case "_":
		return "_", "", false
	case ".":
		return base, "", true
	default:
		alias := strings.TrimSpace(importName.Name)
		if alias == "" {
			return base, base, false
		}
		return alias, alias, false
	}
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, goFileUsages(scan), normalizeDependencyID)
	warnings := make([]string, 0)
	if !stats.HasImports {
		warnings = append(warnings, fmt.Sprintf("no imports found for dependency %q", dependency))
	}

	dep := report.DependencyReport{
		Language:             "go",
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
			Code:     "dot-import",
			Severity: "medium",
			Message:  "dot imports were detected; they can obscure symbol provenance",
		})
	}
	if scan.BlankImportsByDependency[dependency] > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "side-effect-import",
			Severity: "medium",
			Message:  "blank imports were detected; init side effects can hide coupling and startup overhead",
		})
	}
	if scan.UndeclaredImportsByDependency[dependency] > 0 {
		dep.RiskCues = append(dep.RiskCues, report.RiskCue{
			Code:     "undeclared-module-path",
			Severity: "low",
			Message:  "imports resolved to this module but it is not explicitly declared in go.mod",
		})
	}
	dep.Recommendations = buildRecommendations(dep, scan.UndeclaredImportsByDependency[dependency] > 0)
	return dep, warnings
}

func buildRecommendations(dep report.DependencyReport, hasUndeclaredImports bool) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 3)
	if len(dep.UsedImports) == 0 && len(dep.UnusedImports) > 0 {
		recs = append(recs, report.Recommendation{
			Code:      "remove-unused-dependency",
			Priority:  "high",
			Message:   fmt.Sprintf("No used imports were detected for %q; consider removing it.", dep.Name),
			Rationale: "Unused dependencies increase attack and maintenance surface.",
		})
	}
	if shared.HasWildcardImport(dep.UsedImports) || shared.HasWildcardImport(dep.UnusedImports) {
		recs = append(recs, report.Recommendation{
			Code:      "avoid-dot-imports",
			Priority:  "medium",
			Message:   "Dot imports were detected; prefer package-qualified usage for clarity.",
			Rationale: "Qualified imports preserve namespace clarity and improve static analysis precision.",
		})
	}
	if hasUndeclaredImports {
		recs = append(recs, report.Recommendation{
			Code:      "declare-go-module-requirement",
			Priority:  "medium",
			Message:   fmt.Sprintf("Imports for %q were detected without a matching go.mod requirement.", dep.Name),
			Rationale: "Explicit requirements improve reproducibility and make dependency intent clear.",
		})
	}
	return recs
}

func goFileUsages(scan scanResult) []shared.FileUsage {
	return shared.MapFileUsages(
		scan.Files,
		func(file fileScan) []shared.ImportRecord { return file.Imports },
		func(file fileScan) map[string]int { return file.Usage },
	)
}

func loadGoModuleInfo(repoPath string) (moduleInfo, error) {
	info := moduleInfo{
		ReplacementImports: make(map[string]string),
	}

	goModPath := filepath.Join(repoPath, goModName)
	// #nosec G304 -- path is constrained under normalized repoPath.
	content, err := os.ReadFile(goModPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return moduleInfo{}, err
		}
	} else {
		modulePath, dependencies, replacements := parseGoMod(content)
		info.ModulePath = modulePath
		info.DeclaredDependencies = dependencies
		info.ReplacementImports = replacements
		info.LocalModulePaths = append(info.LocalModulePaths, modulePath)
	}

	workModules, err := loadGoWorkLocalModules(repoPath)
	if err != nil {
		return moduleInfo{}, err
	}
	info.LocalModulePaths = append(info.LocalModulePaths, workModules...)

	nestedModules, nestedDeps, nestedReplacements, err := discoverNestedModules(repoPath)
	if err != nil {
		return moduleInfo{}, err
	}
	info.LocalModulePaths = append(info.LocalModulePaths, nestedModules...)
	info.DeclaredDependencies = append(info.DeclaredDependencies, nestedDeps...)
	for replacementImport, dependency := range nestedReplacements {
		if _, ok := info.ReplacementImports[replacementImport]; !ok {
			info.ReplacementImports[replacementImport] = dependency
		}
	}
	info.LocalModulePaths = uniqueStrings(info.LocalModulePaths)
	info.DeclaredDependencies = uniqueStrings(info.DeclaredDependencies)
	sort.Strings(info.LocalModulePaths)
	sort.Strings(info.DeclaredDependencies)

	return info, nil
}

func parseGoMod(content []byte) (string, []string, map[string]string) {
	state := goModParseState{
		depSet:     make(map[string]struct{}),
		replaceSet: make(map[string]string),
	}
	for _, rawLine := range strings.Split(string(content), "\n") {
		processGoModLine(strings.TrimSpace(stripInlineComment(rawLine)), &state)
	}

	dependencies := make([]string, 0, len(state.depSet))
	for dep := range state.depSet {
		dependencies = append(dependencies, dep)
	}
	sort.Strings(dependencies)
	return state.modulePath, dependencies, state.replaceSet
}

type goModParseState struct {
	modulePath     string
	depSet         map[string]struct{}
	replaceSet     map[string]string
	inRequireBlock bool
	inReplaceBlock bool
}

func processGoModLine(line string, state *goModParseState) {
	if line == "" || state == nil {
		return
	}
	if parseGoModModuleLine(line, state) {
		return
	}
	if parseGoModRequireBlockControl(line, state) {
		return
	}
	if parseGoModReplaceBlockControl(line, state) {
		return
	}
	if state.inReplaceBlock {
		addGoModReplacement(line, state.replaceSet)
		return
	}
	if state.inRequireBlock {
		addGoModDependency(line, state.depSet)
		return
	}
	parseGoModSingleRequire(line, state.depSet)
	parseGoModSingleReplace(line, state.replaceSet)
}

func parseGoModModuleLine(line string, state *goModParseState) bool {
	if !strings.HasPrefix(line, "module ") {
		return false
	}
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		state.modulePath = fields[1]
	}
	return true
}

func parseGoModRequireBlockControl(line string, state *goModParseState) bool {
	if strings.HasPrefix(line, "require (") {
		state.inRequireBlock = true
		return true
	}
	if state.inRequireBlock && line == ")" {
		state.inRequireBlock = false
		return true
	}
	return false
}

func parseGoModReplaceBlockControl(line string, state *goModParseState) bool {
	if strings.HasPrefix(line, "replace (") {
		state.inReplaceBlock = true
		return true
	}
	if state.inReplaceBlock && line == ")" {
		state.inReplaceBlock = false
		return true
	}
	return false
}

func parseGoModSingleRequire(line string, depSet map[string]struct{}) {
	if !strings.HasPrefix(line, "require ") {
		return
	}
	addGoModDependency(strings.TrimPrefix(line, "require "), depSet)
}

func parseGoModSingleReplace(line string, replaceSet map[string]string) {
	if !strings.HasPrefix(line, "replace ") {
		return
	}
	addGoModReplacement(strings.TrimPrefix(line, "replace "), replaceSet)
}

func addGoModDependency(line string, depSet map[string]struct{}) {
	if depSet == nil {
		return
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return
	}
	depSet[fields[0]] = struct{}{}
}

func addGoModReplacement(line string, replaceSet map[string]string) {
	if replaceSet == nil {
		return
	}
	parts := strings.SplitN(line, "=>", 2)
	if len(parts) != 2 {
		return
	}
	oldPath := firstToken(parts[0])
	newPath := firstToken(parts[1])
	if oldPath == "" || newPath == "" {
		return
	}
	if isLocalReplaceTarget(newPath) {
		return
	}
	// Track only import-like replacement targets.
	if !looksExternalImport(newPath) {
		return
	}
	replaceSet[newPath] = oldPath
}

func isLocalReplaceTarget(pathValue string) bool {
	pathValue = strings.TrimSpace(pathValue)
	if pathValue == "" {
		return false
	}
	if strings.HasPrefix(pathValue, "./") || strings.HasPrefix(pathValue, "../") || strings.HasPrefix(pathValue, "/") {
		return true
	}
	if len(pathValue) >= 2 && pathValue[1] == ':' {
		return true
	}
	return false
}

func firstToken(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func loadGoWorkLocalModules(repoPath string) ([]string, error) {
	workPath := filepath.Join(repoPath, goWorkName)
	// #nosec G304 -- path is constrained under normalized repoPath.
	content, err := os.ReadFile(workPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	modulePaths := make([]string, 0)
	for _, rel := range parseGoWorkUseEntries(content) {
		if rel == "" {
			continue
		}
		modulePath, _, _, err := loadGoModFromDir(filepath.Join(repoPath, rel))
		if err != nil || modulePath == "" {
			continue
		}
		modulePaths = append(modulePaths, modulePath)
	}
	return uniqueStrings(modulePaths), nil
}

func parseGoWorkUseEntries(content []byte) []string {
	entries := make([]string, 0)
	inUseBlock := false
	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(stripInlineComment(rawLine))
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "use ("):
			inUseBlock = true
		case inUseBlock && line == ")":
			inUseBlock = false
		case inUseBlock:
			entries = append(entries, normalizeGoWorkPath(line))
		case strings.HasPrefix(line, "use "):
			entries = append(entries, normalizeGoWorkPath(strings.TrimPrefix(line, "use ")))
		}
	}
	return uniqueStrings(entries)
}

func normalizeGoWorkPath(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"")
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func loadGoModFromDir(dir string) (string, []string, map[string]string, error) {
	goModPath := filepath.Join(dir, goModName)
	// #nosec G304 -- path is constrained under the caller-controlled repo path.
	content, err := os.ReadFile(goModPath)
	if err != nil {
		return "", nil, nil, err
	}
	modulePath, dependencies, replacements := parseGoMod(content)
	return modulePath, dependencies, replacements, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func stripInlineComment(line string) string {
	if index := strings.Index(line, "//"); index >= 0 {
		return line[:index]
	}
	return line
}

func normalizeDependencyID(value string) string {
	return strings.TrimSpace(strings.ToLower(value))
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".idea", "node_modules", "vendor", "dist", "build", "bin", ".artifacts":
		return true
	default:
		return false
	}
}
