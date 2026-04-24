package dart

import (
	"context"
	"errors"
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
	return scanRepoWithOptions(ctx, repoPath, manifests, false)
}

func scanRepoWithOptions(ctx context.Context, repoPath string, manifests []packageManifest, includeLocalPathImports bool) (scanResult, error) {
	result := scanResult{
		DeclaredDependencies:    make(map[string]dependencyInfo),
		UnresolvedImports:       make(map[string]int),
		includeLocalPathImports: includeLocalPathImports,
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
	}

	for _, manifest := range manifests {
		stop, err := scanManifestRoot(ctx, repoPath, manifest, allRoots, scannedFiles, &fileCount, &result)
		if err != nil {
			return scanResult{}, err
		}
		if stop {
			break
		}
	}

	if result.HasFlutterProject && !result.HasPluginMetadata {
		result.Warnings = append(result.Warnings, "flutter plugin metadata not found in local manifests; plugin classification uses conservative heuristics")
	}
	result.Warnings = append(result.Warnings, compileScanWarnings(result)...)
	result.Warnings = dedupeWarnings(result.Warnings)
	return result, nil
}

func scanManifestRoot(ctx context.Context, repoPath string, manifest packageManifest, allRoots map[string]struct{}, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) (bool, error) {
	if result.SkippedFilesByBound {
		return true, nil
	}
	err := scanPackageRoot(ctx, repoPath, manifest, allRoots, scannedFiles, fileCount, result)
	switch {
	case result.SkippedFilesByBound, errors.Is(err, fs.SkipAll):
		return true, nil
	case err == nil:
		return false, nil
	default:
		return false, err
	}
}

func collectManifestRoots(manifests []packageManifest) map[string]struct{} {
	roots := make(map[string]struct{}, len(manifests))
	for _, manifest := range manifests {
		root := filepath.Clean(strings.TrimSpace(manifest.Root))
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
		repoPath:                repoPath,
		root:                    root,
		depLookup:               manifest.Dependencies,
		allRoots:                allRoots,
		scannedFiles:            scannedFiles,
		fileCount:               fileCount,
		result:                  result,
		includeLocalPathImports: result.includeLocalPathImports,
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		return walkPackageEntry(ctx, scanner, path, entry, walkErr)
	})
}

type packageRootScanner struct {
	repoPath                string
	root                    string
	depLookup               map[string]dependencyInfo
	allRoots                map[string]struct{}
	scannedFiles            map[string]struct{}
	fileCount               *int
	result                  *scanResult
	includeLocalPathImports bool
}

func walkPackageEntry(ctx context.Context, scanner packageRootScanner, path string, entry fs.DirEntry, walkErr error) error {
	if err := walkContextErr(ctx, walkErr); err != nil {
		return err
	}
	if entry.IsDir() {
		return scanPackageDir(scanner.root, path, entry.Name(), scanner.allRoots)
	}
	return scanPackageFileEntryWithOptions(scanner.repoPath, path, scanner.depLookup, scanner.scannedFiles, scanner.fileCount, scanner.result, scanner.includeLocalPathImports)
}

func walkContextErr(ctx context.Context, walkErr error) error {
	return shared.WalkContextErr(ctx, walkErr)
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
	return scanPackageFileEntryWithOptions(repoPath, path, depLookup, scannedFiles, fileCount, result, false)
}

func scanPackageFileEntryWithOptions(repoPath string, path string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult, includeLocalPathImports bool) error {
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
	return scanDartSourceFileWithOptions(repoPath, cleanPath, depLookup, result, includeLocalPathImports)
}

func scanDartSourceFile(repoPath, path string, depLookup map[string]dependencyInfo, result *scanResult) error {
	return scanDartSourceFileWithOptions(repoPath, path, depLookup, result, false)
}

func scanDartSourceFileWithOptions(repoPath, path string, depLookup map[string]dependencyInfo, result *scanResult, includeLocalPathImports bool) error {
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
	relativePath := path
	if relative, relErr := filepath.Rel(repoPath, path); relErr == nil && strings.TrimSpace(relative) != "" {
		relativePath = relative
	}
	imports := parseDartImportsWithOptions(content, relativePath, depLookup, result.UnresolvedImports, includeLocalPathImports)
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Imports: imports,
		Usage:   shared.CountUsage(content, imports),
	})
	return nil
}

func parseDartImports(content []byte, filePath string, depLookup map[string]dependencyInfo, unresolved map[string]int) []importBinding {
	return parseDartImportsWithOptions(content, filePath, depLookup, unresolved, false)
}

func parseDartImportsWithOptions(content []byte, filePath string, depLookup map[string]dependencyInfo, unresolved map[string]int, includeLocalPathImports bool) []importBinding {
	lines := strings.Split(string(content), "\n")
	imports := make([]importBinding, 0)
	for i := 0; i < len(lines); i++ {
		directive, consumed, ok := collectDirective(lines[i:])
		if !ok {
			continue
		}
		lineIndex := i
		i += consumed - 1
		kind, module, clause, ok := parseImportDirective(directive)
		if !ok {
			continue
		}

		dependency := resolveDependencyFromModuleWithOptions(module, depLookup, unresolved, includeLocalPathImports)
		if dependency == "" {
			continue
		}
		location := report.Location{
			File:   filePath,
			Line:   lineIndex + 1,
			Column: shared.FirstContentColumn(lines[lineIndex]),
		}
		imports = append(imports, buildDirectiveBindings(kind, module, clause, dependency, location)...)
	}
	return imports
}

func collectDirective(lines []string) (string, int, bool) {
	if len(lines) == 0 || !directiveStartPattern.MatchString(lines[0]) {
		return "", 1, false
	}
	if hasDirectiveTerminator(lines[0]) {
		return lines[0], 1, true
	}

	directive := lines[0]
	for i := 1; i < len(lines); i++ {
		if !isDirectiveContinuationLine(lines[i]) {
			return "", 1, false
		}
		directive += "\n" + lines[i]
		if hasDirectiveTerminator(lines[i]) {
			return directive, i + 1, true
		}
	}
	return "", 1, false
}

func hasDirectiveTerminator(line string) bool {
	state := directiveTerminatorState{}
	for i := 0; i < len(line); i++ {
		character := line[i]
		if state.consumeQuoted(character) {
			continue
		}

		if directiveCommentStarts(line, i) {
			return false
		}
		if character == ';' {
			return hasOnlyDirectiveTrailingContent(line[i+1:])
		}
		state.openQuote(character)
	}
	return false
}

type directiveTerminatorState struct {
	quote   byte
	escaped bool
}

func (s *directiveTerminatorState) consumeQuoted(character byte) bool {
	if s.quote == 0 {
		return false
	}
	if s.escaped {
		s.escaped = false
		return true
	}
	switch character {
	case '\\':
		s.escaped = true
	case s.quote:
		s.quote = 0
	}
	return true
}

func (s *directiveTerminatorState) openQuote(character byte) {
	if character == '\'' || character == '"' {
		s.quote = character
	}
}

func directiveCommentStarts(line string, index int) bool {
	return line[index] == '/' && index+1 < len(line) && line[index+1] == '/'
}

func hasOnlyDirectiveTrailingContent(suffix string) bool {
	trimmed := strings.TrimSpace(suffix)
	return trimmed == "" || strings.HasPrefix(trimmed, "//")
}

func isDirectiveContinuationLine(line string) bool {
	if line == "" {
		return true
	}
	return line[0] == ' ' || line[0] == '\t'
}

func parseImportDirective(line string) (string, string, string, bool) {
	match := directivePattern.FindStringSubmatch(line)
	if len(match) != 4 {
		return "", "", "", false
	}
	kind := strings.TrimSpace(strings.ToLower(match[1]))
	module := strings.TrimSpace(match[2])
	clause := strings.TrimSpace(match[3])
	if !isValidDirectiveClause(clause) {
		return "", "", "", false
	}
	return kind, module, clause, true
}

func isValidDirectiveClause(clause string) bool {
	remaining := strings.TrimSpace(clause)
	for remaining != "" {
		switch {
		case directiveAliasClausePattern.MatchString(remaining):
			remaining = strings.TrimSpace(directiveAliasClausePattern.ReplaceAllString(remaining, ""))
		case directiveCombinatorClausePattern.MatchString(remaining):
			remaining = strings.TrimSpace(directiveCombinatorClausePattern.ReplaceAllString(remaining, ""))
		default:
			return false
		}
	}
	return true
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
	return strings.TrimSpace(match[1])
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
	return resolveDependencyFromModuleWithOptions(module, depLookup, unresolved, false)
}

func resolveDependencyFromModuleWithOptions(module string, depLookup map[string]dependencyInfo, unresolved map[string]int, includeLocalPathImports bool) string {
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
		if info.LocalPath && !includeLocalPathImports {
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
