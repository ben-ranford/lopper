package golang

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("go", []string{"golang"}, adapter.DetectWithConfidence)
	return adapter
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

	moduleInfo, err := loadGoModuleInfo(repoPath, moduleLoadOptions{
		EnableVendoredProvenance: req.Features.Enabled(goVendoredProvenancePreviewFeature),
	})
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
	return shared.BuildRequestedDependenciesWithWeights(req, scan, normalizeDependencyID, buildDependencyReport, resolveRemovalCandidateWeights, buildTopGoDependencies)
}

func buildTopGoDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	importRecords := func(file fileScan) []shared.ImportRecord { return file.Imports }
	usageRecords := func(file fileScan) map[string]int { return file.Usage }
	fileUsages := shared.MapFileUsages(scan.Files, importRecords, usageRecords)
	dependencies := shared.ListDependencies(fileUsages, normalizeDependencyID)
	return buildTopGoReports(topN, dependencies, scan, weights)
}

func buildTopGoReports(topN int, dependencies []string, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	builder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	return shared.BuildTopReports(topN, dependencies, builder, weights)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func scanRepo(ctx context.Context, repoPath string, moduleInfo moduleInfo) (scanResult, error) {
	result := newScanResult()
	if repoPath == "" {
		return result, fs.ErrInvalid
	}
	workspaceMemberDirs, err := workspaceRootModuleDirs(repoPath, moduleInfo)
	if err != nil {
		return result, err
	}
	nestedModules, err := nestedModuleDirs(repoPath, workspaceMemberDirs)
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
		DependencyProvenanceByDep:     make(map[string]goDependencyProvenance),
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
	if len(moduleInfo.DeclaredDependencies) == 0 && len(moduleInfo.VendoredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no Go dependencies discovered from go.mod")
	}
	result.Warnings = append(result.Warnings, moduleInfo.VendoringWarnings...)
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
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d nested module directories while scanning root module", result.SkippedNestedModuleDirs))
	}
}

func appendUndeclaredDependencyWarnings(result *scanResult) {
	if result == nil {
		return
	}
	for dependency, count := range result.UndeclaredImportsByDependency {
		result.Warnings = append(result.Warnings, fmt.Sprintf("found %d import(s) mapped to %q that are not declared in go.mod", count, dependency))
	}
}

func scanGoSourceFile(repoPath, path string, moduleInfo moduleInfo, result *scanResult) error {
	content, err := safeio.ReadFileUnderLimit(repoPath, path, maxScannableGoFile)
	if errors.Is(err, safeio.ErrFileTooLarge) {
		if result != nil {
			result.SkippedLargeFiles++
		}
		return nil
	}
	if err != nil {
		return err
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

type importMetadata struct {
	Dependency string
	IsBlank    bool
	Undeclared bool
	Provenance goDependencyProvenance
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

		resolved := resolveDependencyFromImport(importPath, moduleInfo)
		if resolved.Dependency == "" {
			continue
		}

		name, local, wildcard := importBindingIdentity(importPath, imported.Name)
		position := fileSet.Position(imported.Pos())
		bindings = append(bindings, importBinding{
			Dependency: resolved.Dependency,
			Module:     importPath,
			Name:       name,
			Local:      local,
			Wildcard:   wildcard,
			Location:   shared.Location(relativePath, position.Line, position.Column),
		})
		metadata = append(metadata, importMetadata{
			Dependency: resolved.Dependency,
			IsBlank:    imported.Name != nil && imported.Name.Name == "_",
			Undeclared: !isDeclaredDependency(resolved.Dependency, moduleInfo.DeclaredDependencies),
			Provenance: resolved.Provenance,
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
		current := result.DependencyProvenanceByDep[item.Dependency]
		current.Declared = current.Declared || item.Provenance.Declared
		current.Replacement = current.Replacement || item.Provenance.Replacement
		current.Vendored = current.Vendored || item.Provenance.Vendored
		result.DependencyProvenanceByDep[item.Dependency] = current
	}
}

func dependencyFromImport(importPath string, moduleInfo moduleInfo) string {
	return resolveDependencyFromImport(importPath, moduleInfo).Dependency
}

type resolvedGoDependency struct {
	Dependency string
	Provenance goDependencyProvenance
}

func resolveDependencyFromImport(importPath string, moduleInfo moduleInfo) resolvedGoDependency {
	importPath = strings.TrimSpace(importPath)
	if importPath == "" || importPath == "C" {
		return resolvedGoDependency{}
	}
	if isLocalModuleImport(importPath, moduleInfo.LocalModulePaths) {
		return resolvedGoDependency{}
	}
	if !looksExternalImport(importPath) {
		return resolvedGoDependency{}
	}
	vendoredDependency := ""
	if moduleInfo.VendoredProvenanceEnabled {
		vendoredDependency = normalizeDependencyID(longestVendoredDependency(importPath, moduleInfo.VendoredImportDependencies))
	}
	if dependency := longestDeclaredDependency(importPath, moduleInfo.DeclaredDependencies); dependency != "" {
		dependency = normalizeDependencyID(dependency)
		return resolvedGoDependency{
			Dependency: dependency,
			Provenance: goDependencyProvenance{
				Declared: true,
				Vendored: vendoredDependency == dependency,
			},
		}
	}
	if dependency := longestReplacementDependency(importPath, moduleInfo.ReplacementImports); dependency != "" {
		dependency = normalizeDependencyID(dependency)
		return resolvedGoDependency{
			Dependency: dependency,
			Provenance: goDependencyProvenance{
				Replacement: true,
				Vendored:    vendoredDependency == dependency,
			},
		}
	}
	if vendoredDependency != "" {
		return resolvedGoDependency{
			Dependency: vendoredDependency,
			Provenance: goDependencyProvenance{Vendored: true},
		}
	}
	return resolvedGoDependency{
		Dependency: normalizeDependencyID(inferDependency(importPath)),
	}
}

func isLocalModuleImport(importPath string, localModules []string) bool {
	for _, modulePath := range localModules {
		if modulePath == "" {
			continue
		}
		if hasImportPathPrefix(importPath, modulePath) {
			return true
		}
	}
	return false
}

func looksExternalImport(importPath string) bool {
	importPath = strings.TrimSpace(importPath)
	if importPath == "" {
		return false
	}
	parts := strings.Split(importPath, "/")
	return strings.Contains(parts[0], ".")
}

func longestDeclaredDependency(importPath string, declaredDependencies []string) string {
	match := ""
	for _, dependency := range declaredDependencies {
		if !hasImportPathPrefix(importPath, dependency) {
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
		if !hasImportPathPrefix(importPath, replacementImport) {
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

func hasImportPathPrefix(importPath, dependency string) bool {
	return importPath == dependency || strings.HasPrefix(importPath, dependency+"/")
}

func inferDependency(importPath string) string {
	importPath = strings.TrimSpace(importPath)
	if importPath == "" {
		return ""
	}
	parts := strings.Split(importPath, "/")
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
	base := defaultImportBindingName(importPath)
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

func defaultImportBindingName(importPath string) string {
	base := path.Base(importPath)
	if prefix, ok := trimModuleVersionSuffix(base); ok {
		return prefix
	}
	return base
}

func trimModuleVersionSuffix(value string) (string, bool) {
	separator := strings.LastIndex(value, ".")
	if separator <= 0 || separator >= len(value)-1 {
		return "", false
	}
	suffix := value[separator+1:]
	if !isVersionSuffix(suffix) {
		return "", false
	}
	return value[:separator], true
}

func isVersionSuffix(value string) bool {
	if len(value) < 2 || value[0] != 'v' {
		return false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	stats := shared.BuildDependencyStats(dependency, goFileUsages(scan), normalizeDependencyID)
	dep := report.DependencyReport{Language: "go", Name: dependency}
	dep.UsedExportsCount = stats.UsedCount
	dep.TotalExportsCount = stats.TotalCount
	dep.UsedPercent = stats.UsedPercent
	dep.EstimatedUnusedBytes = 0
	dep.TopUsedSymbols = stats.TopSymbols
	dep.UsedImports = stats.UsedImports
	dep.UnusedImports = stats.UnusedImports
	dep.Provenance = buildGoDependencyProvenance(scan.DependencyProvenanceByDep[dependency])

	warnings := dependencyWarnings(dependency, stats.HasImports)
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

func buildGoDependencyProvenance(info goDependencyProvenance) *report.DependencyProvenance {
	if !info.Declared && !info.Replacement && !info.Vendored {
		return nil
	}
	signals := make([]string, 0, 3)
	source := "go.mod"
	confidence := "medium"
	if info.Declared {
		signals = append(signals, goModName)
		confidence = "high"
	}
	if info.Replacement {
		signals = append(signals, "replace")
		source = "go.mod-replace"
		confidence = "high"
	}
	if info.Vendored {
		signals = append(signals, vendorModulesTxtName)
		if info.Declared || info.Replacement {
			source = "go.mod+vendor"
			confidence = "high"
		} else {
			source = "vendor/modules.txt"
			confidence = "medium"
		}
	}
	return &report.DependencyProvenance{
		Source:     source,
		Confidence: confidence,
		Signals:    uniqueStrings(signals),
	}
}

func buildRecommendations(dep report.DependencyReport, hasUndeclaredImports bool) []report.Recommendation {
	recs := make([]report.Recommendation, 0, 3)
	recs = appendUnusedDependencyRecommendation(recs, dep)
	recs = appendDotImportRecommendation(recs, dep)
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

func dependencyWarnings(dependency string, hasImports bool) []string {
	if hasImports {
		return nil
	}
	return []string{fmt.Sprintf("no imports found for dependency %q", dependency)}
}

func appendUnusedDependencyRecommendation(recs []report.Recommendation, dep report.DependencyReport) []report.Recommendation {
	if len(dep.UsedImports) != 0 || len(dep.UnusedImports) == 0 {
		return recs
	}
	return append(recs, report.Recommendation{
		Code:      "remove-unused-dependency",
		Priority:  "high",
		Message:   fmt.Sprintf("No used imports were detected for %q; consider removing it.", dep.Name),
		Rationale: "Unused dependencies increase attack and maintenance surface.",
	})
}

func appendDotImportRecommendation(recs []report.Recommendation, dep report.DependencyReport) []report.Recommendation {
	if !shared.HasWildcardImport(dep.UsedImports) && !shared.HasWildcardImport(dep.UnusedImports) {
		return recs
	}
	return append(recs, report.Recommendation{
		Code:      "avoid-dot-imports",
		Priority:  "medium",
		Message:   "Dot imports were detected; prefer package-qualified usage for clarity.",
		Rationale: "Qualified imports preserve namespace clarity and improve static analysis precision.",
	})
}

func goFileUsages(scan scanResult) []shared.FileUsage {
	return shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
}

func normalizeDependencyID(value string) string {
	return shared.NormalizeDependencyID(value)
}

func shouldSkipDir(name string) bool {
	return shared.ShouldSkipDir(name, goSkippedDirs)
}
