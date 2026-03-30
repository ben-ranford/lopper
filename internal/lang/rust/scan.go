package rust

import (
	"bytes"
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
		if err != nil && !errors.Is(err, fs.SkipAll) {
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
	return shared.WalkRepoFiles(ctx, root, 0, shouldSkipDir, func(path string, entry fs.DirEntry) error {
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

	(*fileCount)++
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
	content, err := safeio.ReadFileUnderLimit(repoPath, path, maxScannableRustFile)
	if errors.Is(err, safeio.ErrFileTooLarge) {
		result.SkippedLargeFiles++
		return nil
	}
	if err != nil {
		return err
	}
	relativePath, err := filepath.Rel(repoPath, path)
	if err != nil {
		relativePath = path
	}

	imports := parseRustImportsBytes(content, relativePath, crateRoot, depLookup, result)
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
	return parseRustImportsBytes([]byte(content), filePath, crateRoot, depLookup, scan)
}

func parseRustImportsBytes(content []byte, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) []importBinding {
	externImports, useImports := collectRustImports(content, filePath, crateRoot, depLookup, scan, true)
	return append(externImports, useImports...)
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

func appendUseClauseImports(imports []importBinding, clause string, ctx useImportContext) []importBinding {
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
	return parseExternCrateImportsBytes([]byte(content), filePath, crateRoot, depLookup, scan)
}

func parseExternCrateImportsBytes(content []byte, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) []importBinding {
	imports, _ := collectRustImports(content, filePath, crateRoot, depLookup, scan, false)
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

type rustImportKind uint8

const (
	rustImportExternCrate rustImportKind = iota
	rustImportUse
)

type rustImportStatement struct {
	Clause []byte
	Line   int
	Column int
}

func collectRustImports(content []byte, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult, includeUse bool) ([]importBinding, []importBinding) {
	externImports := make([]importBinding, 0)
	useImports := make([]importBinding, 0)
	scanRustImportStatements(content, includeUse, func(kind rustImportKind, stmt rustImportStatement) {
		switch kind {
		case rustImportExternCrate:
			binding, ok := parseExternCrateClause(stmt.Clause, filePath, crateRoot, depLookup, scan, stmt.Line, stmt.Column)
			if ok {
				externImports = append(externImports, binding)
			}
		case rustImportUse:
			ctx := useImportContext{
				FilePath:  filePath,
				Line:      stmt.Line,
				Column:    stmt.Column,
				CrateRoot: crateRoot,
				DepLookup: depLookup,
				Scan:      scan,
			}
			useImports = appendUseClauseImports(useImports, string(stmt.Clause), ctx)
		}
	})
	return externImports, useImports
}

func scanRustImportStatements(content []byte, includeUse bool, visit func(rustImportKind, rustImportStatement)) {
	for lineStart, line := 0, 1; lineStart < len(content); line++ {
		lineEnd := lineStart
		for lineEnd < len(content) && content[lineEnd] != '\n' {
			lineEnd++
		}
		if kind, stmt, ok := parseRustImportStatement(content, lineStart, lineEnd, line, includeUse); ok {
			visit(kind, stmt)
		}
		if lineEnd == len(content) {
			break
		}
		lineStart = lineEnd + 1
	}
}

func parseRustImportStatement(content []byte, lineStart, lineEnd, line int, includeUse bool) (rustImportKind, rustImportStatement, bool) {
	currentLine := content[lineStart:lineEnd]
	firstContent := firstContentByteIndex(currentLine)
	if firstContent >= len(currentLine) {
		return 0, rustImportStatement{}, false
	}

	statementStart := lineStart + firstContent
	statement := currentLine[firstContent:]
	if visibilityOffset := skipRustVisibilityPrefix(statement); visibilityOffset > 0 {
		statementStart += visibilityOffset
		statement = statement[visibilityOffset:]
	}
	if len(statement) == 0 {
		return 0, rustImportStatement{}, false
	}

	if includeUse {
		if stmt, ok := buildRustUseStatement(content, lineStart, line, statementStart, statement); ok {
			return rustImportUse, stmt, true
		}
	}
	if stmt, ok := buildRustExternCrateStatement(content, lineEnd, line, firstContent, statementStart, statement); ok {
		return rustImportExternCrate, stmt, true
	}
	return 0, rustImportStatement{}, false
}

func buildRustUseStatement(content []byte, lineStart, line, statementStart int, statement []byte) (rustImportStatement, bool) {
	clauseStart, ok := matchRustUseStatement(statement)
	if !ok {
		return rustImportStatement{}, false
	}

	clauseOffset := skipRustWhitespace(content, statementStart+clauseStart)
	if clauseOffset >= len(content) {
		return rustImportStatement{}, false
	}

	end := bytes.IndexByte(content[clauseOffset:], ';')
	if end < 0 {
		return rustImportStatement{}, false
	}

	stmtLine, column := lineColumnBytesFrom(content, line, lineStart, clauseOffset)
	return rustImportStatement{
		Clause: bytes.TrimSpace(content[clauseOffset : clauseOffset+end]),
		Line:   stmtLine,
		Column: column,
	}, true
}

func buildRustExternCrateStatement(content []byte, lineEnd, line, firstContent, statementStart int, statement []byte) (rustImportStatement, bool) {
	clauseStart, ok := matchExternCrateStatement(statement)
	if !ok {
		return rustImportStatement{}, false
	}

	end := bytes.IndexByte(content[statementStart+clauseStart:lineEnd], ';')
	if end < 0 {
		return rustImportStatement{}, false
	}

	return rustImportStatement{
		Clause: bytes.TrimSpace(content[statementStart+clauseStart : statementStart+clauseStart+end]),
		Line:   line,
		Column: firstContent + 1,
	}, true
}

func matchRustUseStatement(line []byte) (int, bool) {
	if !bytes.HasPrefix(line, []byte("use")) {
		return 0, false
	}
	if len(line) == len("use") {
		return len("use"), true
	}
	if !isRustWhitespace(line[len("use")]) {
		return 0, false
	}
	return len("use"), true
}

func matchExternCrateStatement(line []byte) (int, bool) {
	if !bytes.HasPrefix(line, []byte("extern")) {
		return 0, false
	}
	if len(line) <= len("extern") || !isRustWhitespace(line[len("extern")]) {
		return 0, false
	}
	index := skipRustWhitespace(line, len("extern"))
	if !bytes.HasPrefix(line[index:], []byte("crate")) {
		return 0, false
	}
	index += len("crate")
	if index >= len(line) || !isRustWhitespace(line[index]) {
		return 0, false
	}
	return index, true
}

func parseExternCrateClause(clause []byte, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult, line, column int) (importBinding, bool) {
	clause = bytes.TrimSpace(clause)
	root, offset, ok := consumeRustIdentifier(clause)
	if !ok {
		return importBinding{}, false
	}

	local := root
	rest := bytes.TrimSpace(clause[offset:])
	if len(rest) > 0 {
		if len(rest) <= len("as") || !bytes.HasPrefix(rest, []byte("as")) || !isRustWhitespace(rest[len("as")]) {
			return importBinding{}, false
		}
		aliasClause := bytes.TrimSpace(rest[len("as"):])
		alias, next, ok := consumeRustIdentifier(aliasClause)
		if !ok {
			return importBinding{}, false
		}
		if len(bytes.TrimSpace(aliasClause[next:])) > 0 {
			return importBinding{}, false
		}
		local = alias
	}

	dependency := resolveDependency(root, crateRoot, depLookup, scan)
	if dependency == "" {
		return importBinding{}, false
	}

	return importBinding{
		Dependency: dependency,
		Module:     root,
		Name:       root,
		Local:      local,
		Location: report.Location{
			File:   filePath,
			Line:   line,
			Column: column,
		},
	}, true
}

func consumeRustIdentifier(value []byte) (string, int, bool) {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || !isRustIdentifierStart(value[0]) {
		return "", 0, false
	}
	index := 1
	for index < len(value) && isRustIdentifierContinue(value[index]) {
		index++
	}
	return string(value[:index]), index, true
}

func isRustIdentifierStart(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func isRustIdentifierContinue(b byte) bool {
	return isRustIdentifierStart(b) || (b >= '0' && b <= '9')
}

func firstContentByteIndex(line []byte) int {
	for index := 0; index < len(line); index++ {
		if line[index] != ' ' && line[index] != '\t' {
			return index
		}
	}
	return len(line)
}

func skipRustVisibilityPrefix(line []byte) int {
	if !bytes.HasPrefix(line, []byte("pub")) || len(line) <= len("pub") {
		return 0
	}
	index := len("pub")
	if line[index] == '(' {
		index++
		for index < len(line) && line[index] != ')' {
			index++
		}
		if index >= len(line) {
			return 0
		}
		index++
	}
	if index >= len(line) || !isRustWhitespace(line[index]) {
		return 0
	}
	return skipRustWhitespace(line, index)
}

func skipRustWhitespace(value []byte, index int) int {
	for index < len(value) && isRustWhitespace(value[index]) {
		index++
	}
	return index
}

func isRustWhitespace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

func lineColumnBytesFrom(content []byte, baseLine, baseOffset, offset int) (int, int) {
	if offset < 0 {
		return baseLine, 1
	}
	if offset > len(content) {
		offset = len(content)
	}
	if offset <= baseOffset {
		return baseLine, 1
	}
	segment := content[baseOffset:offset]
	lineDelta := bytes.Count(segment, []byte{'\n'})
	if lineDelta == 0 {
		return baseLine, offset - baseOffset + 1
	}
	lastNewline := bytes.LastIndexByte(segment, '\n')
	lineStart := baseOffset + lastNewline + 1
	return baseLine + lineDelta, offset - lineStart + 1
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
