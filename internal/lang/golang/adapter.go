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

func scanRepo(ctx context.Context, repoPath string, moduleInfo moduleInfo) (scanResult, error) {
	result := newScanResult()
	if repoPath == "" {
		return result, fs.ErrInvalid
	}
	if err := walkGoFiles(ctx, repoPath, moduleInfo, &result); err != nil {
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

func walkGoFiles(ctx context.Context, repoPath string, moduleInfo moduleInfo, result *scanResult) error {
	return filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			return handleScanDirEntry(entry)
		}
		if !strings.EqualFold(filepath.Ext(path), ".go") {
			return nil
		}
		return scanGoSourceFile(repoPath, path, moduleInfo, result)
	})
}

func handleScanDirEntry(entry fs.DirEntry) error {
	if shouldSkipDir(entry.Name()) {
		return filepath.SkipDir
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

func normalizeDependencyID(value string) string {
	return shared.NormalizeDependencyID(value)
}

func shouldSkipDir(name string) bool {
	return shared.ShouldSkipDir(name, goSkippedDirs)
}
