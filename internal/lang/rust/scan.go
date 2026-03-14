package rust

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

func scanRepo(ctx context.Context, repoPath string, manifestPaths []string, depLookup map[string]dependencyInfo, renamedAliases map[string][]string) (scanResult, error) {
	result := scanResult{
		UnresolvedImports:   make(map[string]int),
		RenamedAliasesByDep: renamedAliases,
		LocalModuleCache:    make(map[string]bool),
	}
	roots := scanRoots(manifestPaths, repoPath)
	scannedFiles := make(map[string]struct{})
	fileCount := 0
	for _, root := range roots {
		err := scanRepoRoot(ctx, repoPath, root, depLookup, scannedFiles, &fileCount, &result)
		if err != nil && err != fs.SkipAll {
			return scanResult{}, err
		}
	}
	result.Warnings = append(result.Warnings, compileScanWarnings(result)...)
	result.Warnings = dedupeWarnings(result.Warnings)
	return result, nil
}

func scanRoots(manifestPaths []string, repoPath string) []string {
	roots := make([]string, 0, len(manifestPaths))
	for _, manifestPath := range manifestPaths {
		roots = append(roots, filepath.Dir(manifestPath))
	}
	roots = uniquePaths(roots)
	if len(roots) == 0 {
		return []string{repoPath}
	}
	return roots
}

func scanRepoRoot(ctx context.Context, repoPath, root string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
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
		return scanRepoFileEntry(repoPath, root, path, depLookup, scannedFiles, fileCount, result)
	})
}

func scanRepoFileEntry(repoPath, root, path string, depLookup map[string]dependencyInfo, scannedFiles map[string]struct{}, fileCount *int, result *scanResult) error {
	if !strings.EqualFold(filepath.Ext(path), ".rs") {
		return nil
	}
	if _, ok := scannedFiles[path]; ok {
		return nil
	}
	scannedFiles[path] = struct{}{}

	*fileCount++
	if *fileCount > maxScanFiles {
		result.SkippedFilesByBoundLimit = true
		return fs.SkipAll
	}
	return scanRustSourceFile(repoPath, root, path, depLookup, result)
}

func compileScanWarnings(result scanResult) []string {
	warnings := make([]string, 0, 4+len(result.UnresolvedImports))
	if len(result.Files) == 0 {
		warnings = append(warnings, "no Rust source files found for analysis")
	}
	if result.SkippedLargeFiles > 0 {
		warnings = append(warnings, fmt.Sprintf("skipped %d Rust files larger than %d bytes", result.SkippedLargeFiles, maxScannableRustFile))
	}
	if result.SkippedFilesByBoundLimit {
		warnings = append(warnings, fmt.Sprintf("Rust source scanning capped at %d files", maxScanFiles))
	}
	if result.MacroAmbiguityDetected {
		warnings = append(warnings, "Rust macro invocations detected; static attribution may be partial for macro- and feature-driven paths")
	}
	return append(warnings, summarizeUnresolved(result.UnresolvedImports)...)
}

func scanRustSourceFile(repoPath string, crateRoot string, path string, depLookup map[string]dependencyInfo, result *scanResult) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > maxScannableRustFile {
		result.SkippedLargeFiles++
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

	imports := parseRustImports(string(content), relativePath, crateRoot, depLookup, result)
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Imports: imports,
		Usage:   shared.CountUsage(content, imports),
	})
	if macroInvokePattern.Match(content) {
		result.MacroAmbiguityDetected = true
	}
	return nil
}

func parseRustImports(content, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) []importBinding {
	imports := parseExternCrateImports(content, filePath, crateRoot, depLookup, scan)
	for _, idx := range useStmtPattern.FindAllStringSubmatchIndex(content, -1) {
		clause, line, column, ok := parseUseStatementIndex(content, idx)
		if !ok {
			continue
		}
		ctx := useImportContext{
			FilePath:  filePath,
			Line:      line,
			Column:    column,
			CrateRoot: crateRoot,
			DepLookup: depLookup,
			Scan:      scan,
		}
		imports = append(imports, appendUseClauseImports(clause, ctx)...)
	}
	return imports
}

func parseUseStatementIndex(content string, idx []int) (string, int, int, bool) {
	if len(idx) < 4 {
		return "", 0, 0, false
	}
	clauseStart, clauseEnd := idx[2], idx[3]
	if clauseStart < 0 || clauseEnd < 0 || clauseEnd > len(content) {
		return "", 0, 0, false
	}
	clause := strings.TrimSpace(content[clauseStart:clauseEnd])
	line, column := lineColumn(content, clauseStart)
	return clause, line, column, true
}

func appendUseClauseImports(clause string, ctx useImportContext) []importBinding {
	imports := make([]importBinding, 0)
	entries := parseUseClause(clause)
	for _, entry := range entries {
		binding, ok := makeUseImportBinding(entry, ctx)
		if !ok {
			continue
		}
		imports = append(imports, binding)
	}
	return imports
}

func makeUseImportBinding(entry usePathEntry, ctx useImportContext) (importBinding, bool) {
	if entry.Path == "" {
		return importBinding{}, false
	}
	dependency := resolveDependency(entry.Path, ctx.CrateRoot, ctx.DepLookup, ctx.Scan)
	if dependency == "" {
		return importBinding{}, false
	}
	module := strings.TrimPrefix(entry.Path, "::")
	name, local := normalizeUseSymbolNames(entry, module)
	return importBinding{
		Dependency: dependency,
		Module:     module,
		Name:       name,
		Local:      local,
		Wildcard:   entry.Wildcard,
		Location: report.Location{
			File:   ctx.FilePath,
			Line:   ctx.Line,
			Column: ctx.Column,
		},
	}, true
}

func normalizeUseSymbolNames(entry usePathEntry, module string) (string, string) {
	name := entry.Symbol
	if name == "" {
		name = lastPathSegment(module)
	}
	local := entry.Local
	if local == "" {
		local = name
	}
	if entry.Wildcard {
		name = "*"
		if local == "" {
			local = lastPathSegment(module)
		}
	}
	return name, local
}

func parseExternCrateImports(content, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) []importBinding {
	lines := strings.Split(content, "\n")
	imports := make([]importBinding, 0)
	for i, line := range lines {
		match := externCratePattern.FindStringSubmatch(line)
		if len(match) < 2 {
			continue
		}
		root := strings.TrimSpace(match[1])
		local := root
		if len(match) >= 3 && strings.TrimSpace(match[2]) != "" {
			local = strings.TrimSpace(match[2])
		}
		dependency := resolveDependency(root, crateRoot, depLookup, scan)
		if dependency == "" {
			continue
		}
		imports = append(imports, importBinding{
			Dependency: dependency,
			Module:     root,
			Name:       root,
			Local:      local,
			Location: report.Location{
				File:   filePath,
				Line:   i + 1,
				Column: shared.FirstContentColumn(line),
			},
		})
	}
	return imports
}

func parseUseClause(clause string) []usePathEntry {
	parts := splitTopLevel(clause, ',')
	entries := make([]usePathEntry, 0)
	for _, part := range parts {
		expandUsePart(strings.TrimSpace(part), "", &entries)
	}
	return entries
}

func expandUsePart(part, prefix string, out *[]usePathEntry) {
	part = strings.TrimSpace(part)
	if part == "" {
		return
	}
	part = strings.TrimPrefix(part, "pub ")
	if expandUseBraceGroup(part, prefix, out) {
		return
	}
	if expandUsePrefixedBraceGroup(part, prefix, out) {
		return
	}
	part, local := parseUseLocalAlias(part)
	part, prefix, wildcard := normalizeUseWildcard(part, prefix)
	*out = append(*out, makeUsePathEntry(prefix, part, local, wildcard))
}

func expandUseBraceGroup(part, prefix string, out *[]usePathEntry) bool {
	if !strings.HasPrefix(part, "{") || !strings.HasSuffix(part, "}") {
		return false
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}"))
	expandUseSegments(inner, prefix, out)
	return true
}

func expandUsePrefixedBraceGroup(part, prefix string, out *[]usePathEntry) bool {
	idx := strings.Index(part, "::{")
	if idx < 0 || !strings.HasSuffix(part, "}") {
		return false
	}
	base := strings.TrimSpace(part[:idx])
	inner := strings.TrimSpace(part[idx+3 : len(part)-1])
	nextPrefix := joinPath(prefix, base)
	expandUseSegments(inner, nextPrefix, out)
	return true
}

func expandUseSegments(inner, prefix string, out *[]usePathEntry) {
	for _, segment := range splitTopLevel(inner, ',') {
		expandUsePart(segment, prefix, out)
	}
}

func parseUseLocalAlias(part string) (string, string) {
	idx := strings.LastIndex(part, " as ")
	if idx <= 0 {
		return part, ""
	}
	local := strings.TrimSpace(part[idx+4:])
	base := strings.TrimSpace(part[:idx])
	return base, local
}

func normalizeUseWildcard(part, prefix string) (string, string, bool) {
	wildcard := part == "*" || strings.HasSuffix(part, "::*")
	if !wildcard {
		return part, prefix, false
	}
	if part == "*" {
		return strings.TrimSpace(prefix), "", true
	}
	return strings.TrimSpace(strings.TrimSuffix(part, "::*")), prefix, true
}

func makeUsePathEntry(prefix, part, local string, wildcard bool) usePathEntry {
	fullPath := joinPath(prefix, part)
	symbol := lastPathSegment(fullPath)
	if strings.EqualFold(symbol, "self") {
		symbol = lastPathSegment(prefix)
	}
	if wildcard {
		symbol = "*"
	}
	if strings.EqualFold(local, "self") {
		local = lastPathSegment(prefix)
	}
	return usePathEntry{
		Path:     fullPath,
		Symbol:   symbol,
		Local:    local,
		Wildcard: wildcard,
	}
}

func joinPath(prefix, value string) string {
	prefix = strings.TrimSpace(prefix)
	value = strings.TrimSpace(value)
	switch {
	case prefix == "":
		return strings.TrimPrefix(value, "::")
	case value == "":
		return strings.TrimPrefix(prefix, "::")
	default:
		return strings.TrimPrefix(prefix+"::"+value, "::")
	}
}

func splitTopLevel(value string, sep rune) []string {
	parts := make([]string, 0)
	depth := 0
	start := 0
	for i, r := range value {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(value[start:i]))
				start = i + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

func resolveDependency(path string, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) string {
	path = strings.TrimSpace(strings.TrimPrefix(path, "::"))
	if path == "" {
		return ""
	}
	root := strings.Split(path, "::")[0]
	normalizedRoot := normalizeDependencyID(root)
	if normalizedRoot == "" {
		return ""
	}
	if rustStdRoots[normalizedRoot] {
		return ""
	}
	if normalizedRoot == "crate" || normalizedRoot == "self" || normalizedRoot == "super" {
		return ""
	}
	if isLocalRustModuleWithCache(scan, crateRoot, root) {
		return ""
	}

	if info, ok := depLookup[normalizedRoot]; ok {
		if info.LocalPath {
			return ""
		}
		return info.Canonical
	}
	if scan != nil {
		scan.UnresolvedImports[normalizedRoot]++
	}
	return normalizedRoot
}

func isLocalRustModuleWithCache(scan *scanResult, crateRoot, root string) bool {
	if scan == nil {
		return isLocalRustModule(crateRoot, root)
	}
	if scan.LocalModuleCache == nil {
		scan.LocalModuleCache = make(map[string]bool)
	}
	key := crateRoot + localModuleCacheSep + root
	if cached, ok := scan.LocalModuleCache[key]; ok {
		return cached
	}
	isLocal := isLocalRustModule(crateRoot, root)
	scan.LocalModuleCache[key] = isLocal
	return isLocal
}

func isLocalRustModule(crateRoot, root string) bool {
	if crateRoot == "" || root == "" {
		return false
	}
	candidates := []string{
		filepath.Join(crateRoot, "src", root+".rs"),
		filepath.Join(crateRoot, "src", root, "mod.rs"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return true
		}
	}
	return false
}

func lineColumn(content string, offset int) (int, int) {
	if offset < 0 {
		return 1, 1
	}
	if offset > len(content) {
		offset = len(content)
	}
	line := 1 + strings.Count(content[:offset], "\n")
	lineStart := strings.LastIndex(content[:offset], "\n")
	if lineStart < 0 {
		return line, offset + 1
	}
	return line, offset - lineStart
}
