package dart

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

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

	scanner := packageRootScanner{
		repoPath:     repoPath,
		root:         root,
		depLookup:    manifest.Dependencies,
		allRoots:     allRoots,
		scannedFiles: scannedFiles,
		fileCount:    fileCount,
		result:       result,
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		return walkPackageEntry(ctx, scanner, path, entry, walkErr)
	})
}

type packageRootScanner struct {
	repoPath     string
	root         string
	depLookup    map[string]dependencyInfo
	allRoots     map[string]struct{}
	scannedFiles map[string]struct{}
	fileCount    *int
	result       *scanResult
}

func walkPackageEntry(ctx context.Context, scanner packageRootScanner, path string, entry fs.DirEntry, walkErr error) error {
	if err := walkContextErr(ctx, walkErr); err != nil {
		return err
	}
	if entry.IsDir() {
		return scanPackageDir(scanner.root, path, entry.Name(), scanner.allRoots)
	}
	return scanPackageFileEntry(scanner.repoPath, path, scanner.depLookup, scanner.scannedFiles, scanner.fileCount, scanner.result)
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
