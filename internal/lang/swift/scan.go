package swift

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func scanRepo(ctx context.Context, repoPath string, catalog dependencyCatalog) (scanResult, error) {
	scanner := newRepoScanner(repoPath, catalog)
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		return scanner.walk(ctx, path, entry, walkErr)
	})
	if err != nil && err != fs.SkipAll {
		return scanner.scan, err
	}
	scanner.finalize()
	return scanner.scan, nil
}

func newRepoScanner(repoPath string, catalog dependencyCatalog) *repoScanner {
	scan := scanResult{
		KnownDependencies:    make(map[string]struct{}),
		ImportedDependencies: make(map[string]struct{}),
	}
	for dependency := range catalog.Dependencies {
		scan.KnownDependencies[dependency] = struct{}{}
	}
	return &repoScanner{
		repoPath:          repoPath,
		catalog:           catalog,
		scan:              scan,
		unresolvedImports: make(map[string]int),
	}
}

func (s *repoScanner) walk(ctx context.Context, path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if entry.IsDir() {
		return maybeSkipSwiftDir(entry.Name())
	}
	if !strings.EqualFold(filepath.Ext(entry.Name()), ".swift") {
		return nil
	}
	return s.scanSwiftFile(path, entry)
}

func (s *repoScanner) scanSwiftFile(path string, entry fs.DirEntry) error {
	s.foundSwift = true
	s.visited++
	if s.visited > maxScanFiles {
		return fs.SkipAll
	}
	if isLargeSwiftFile(entry) {
		s.skippedLargeFiles++
		return nil
	}

	content, err := safeio.ReadFileUnder(s.repoPath, path)
	if err != nil {
		return err
	}
	relPath := s.relativePath(path, entry.Name())
	mappedImports := s.resolveImports(parseSwiftImports(content, relPath))
	s.scan.Files = append(s.scan.Files, fileScan{
		Path:    relPath,
		Imports: mappedImports,
		Usage:   applyUnqualifiedUsageHeuristic(content, mappedImports, shared.CountUsage(content, mappedImports)),
	})
	return nil
}

func isLargeSwiftFile(entry fs.DirEntry) bool {
	info, err := entry.Info()
	return err == nil && info.Size() > maxScannableSwiftFile
}

func (s *repoScanner) relativePath(path, fallback string) string {
	relPath, err := filepath.Rel(s.repoPath, path)
	if err != nil {
		return fallback
	}
	return relPath
}

func (s *repoScanner) resolveImports(imports []importBinding) []importBinding {
	mappedImports := make([]importBinding, 0, len(imports))
	for _, imported := range imports {
		dependency := resolveImportDependency(s.catalog, imported.Module)
		if dependency == "" {
			s.recordUnresolvedImport(imported.Module)
			continue
		}
		imported.Dependency = dependency
		if imported.Name == "" {
			imported.Name = imported.Module
		}
		if imported.Local == "" {
			imported.Local = imported.Name
		}
		s.scan.ImportedDependencies[dependency] = struct{}{}
		mappedImports = append(mappedImports, imported)
	}
	return mappedImports
}

func (s *repoScanner) recordUnresolvedImport(module string) {
	if shouldTrackUnresolvedImport(module, s.catalog) {
		s.unresolvedImports[module]++
	}
}

func (s *repoScanner) finalize() {
	if !s.foundSwift {
		s.scan.Warnings = append(s.scan.Warnings, "no Swift files found for analysis")
	}
	if s.visited >= maxScanFiles {
		s.scan.Warnings = append(s.scan.Warnings, fmt.Sprintf("Swift scan capped at %d files", maxScanFiles))
	}
	if s.skippedLargeFiles > 0 {
		s.scan.Warnings = append(s.scan.Warnings, fmt.Sprintf("skipped %d Swift file(s) larger than %d bytes", s.skippedLargeFiles, maxScannableSwiftFile))
	}
	if len(s.unresolvedImports) > 0 {
		s.scan.Warnings = append(s.scan.Warnings, unresolvedImportWarning(s.unresolvedImports))
	}
}

func unresolvedImportWarning(unresolved map[string]int) string {
	type unresolvedEntry struct {
		Module string
		Count  int
	}
	entries := make([]unresolvedEntry, 0, len(unresolved))
	for module, count := range unresolved {
		entries = append(entries, unresolvedEntry{Module: module, Count: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count == entries[j].Count {
			return entries[i].Module < entries[j].Module
		}
		return entries[i].Count > entries[j].Count
	})
	samples := make([]string, 0, maxWarningSamples)
	for index, item := range entries {
		if index >= maxWarningSamples {
			break
		}
		samples = append(samples, fmt.Sprintf("%s (%d)", item.Module, item.Count))
	}
	if len(entries) > maxWarningSamples {
		samples = append(samples, fmt.Sprintf("+%d more", len(entries)-maxWarningSamples))
	}
	return "could not map some Swift imports to Package.swift/Package.resolved dependencies: " + strings.Join(samples, ", ")
}

func shouldTrackUnresolvedImport(module string, catalog dependencyCatalog) bool {
	if len(catalog.Dependencies) == 0 {
		return false
	}
	key := lookupKey(module)
	if key == "" {
		return false
	}
	if _, ok := catalog.LocalModules[key]; ok {
		return false
	}
	if _, ok := standardSwiftSymbols[key]; ok {
		return false
	}
	return true
}

func parseSwiftImports(content []byte, filePath string) []importBinding {
	return shared.ParseImportLines(content, filePath, func(line string, index int) []shared.ImportRecord {
		line = shared.StripLineComment(line, "//")
		matches := swiftImportPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			return nil
		}
		moduleName := strings.TrimSpace(matches[1])
		if moduleName == "" {
			return nil
		}
		return []shared.ImportRecord{{
			Module:   moduleName,
			Name:     moduleName,
			Local:    moduleName,
			Wildcard: false,
			Location: shared.LocationFromLine(filePath, index, line),
		}}
	})
}

func applyUnqualifiedUsageHeuristic(content []byte, imports []importBinding, usage map[string]int) map[string]int {
	if len(imports) == 0 {
		return usage
	}
	byDependency := importsByDependency(imports)
	// Unqualified symbol usage cannot be reliably attributed when a file imports
	// multiple third-party dependencies.
	if len(byDependency) != 1 {
		return usage
	}
	for _, importsForDependency := range byDependency {
		if hasQualifiedImportUsage(importsForDependency, usage) {
			return usage
		}
		if !hasPotentialUnqualifiedSymbolUsage(content, importsForDependency) {
			return usage
		}
		seedUnqualifiedUsage(importsForDependency, usage)
	}
	return usage
}

func hasPotentialUnqualifiedSymbolUsage(content []byte, imports []importBinding) bool {
	importModules := importedModuleSet(imports)
	localDeclaredSymbols := collectLocalDeclaredSymbols(content)
	lines := swiftSymbolScanLines(content)
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || swiftImportPattern.MatchString(line) {
			continue
		}
		if lineHasPotentialUnqualifiedSymbolUsage(line, importModules, localDeclaredSymbols) {
			return true
		}
	}
	return false
}

func importsByDependency(imports []importBinding) map[string][]importBinding {
	byDependency := make(map[string][]importBinding)
	for _, imported := range imports {
		dependency := normalizeDependencyID(imported.Dependency)
		if dependency == "" {
			continue
		}
		byDependency[dependency] = append(byDependency[dependency], imported)
	}
	return byDependency
}

func hasQualifiedImportUsage(imports []importBinding, usage map[string]int) bool {
	for _, imported := range imports {
		if imported.Local != "" && usage[imported.Local] > 0 {
			return true
		}
	}
	return false
}

func seedUnqualifiedUsage(imports []importBinding, usage map[string]int) {
	for _, imported := range imports {
		if imported.Local != "" && usage[imported.Local] == 0 {
			usage[imported.Local] = 1
		}
	}
}

func importedModuleSet(imports []importBinding) map[string]struct{} {
	importModules := make(map[string]struct{}, len(imports))
	for _, imported := range imports {
		key := lookupKey(imported.Module)
		if key != "" {
			importModules[key] = struct{}{}
		}
	}
	return importModules
}

func lineHasPotentialUnqualifiedSymbolUsage(line string, importModules map[string]struct{}, localDeclaredSymbols map[string]struct{}) bool {
	symbols := swiftUpperIdentifierPattern.FindAllString(line, -1)
	for _, symbol := range symbols {
		key := lookupKey(symbol)
		if isIgnoredUnqualifiedSymbol(key, importModules, localDeclaredSymbols) {
			continue
		}
		return true
	}
	return false
}

func isIgnoredUnqualifiedSymbol(key string, importModules map[string]struct{}, localDeclaredSymbols map[string]struct{}) bool {
	if key == "" {
		return true
	}
	if _, ok := importModules[key]; ok {
		return true
	}
	if _, ok := localDeclaredSymbols[key]; ok {
		return true
	}
	if _, ok := standardSwiftSymbols[key]; ok {
		return true
	}
	return false
}

func swiftSymbolScanLines(content []byte) []string {
	return strings.Split(blankSwiftStringsAndComments(content), "\n")
}

func blankSwiftStringsAndComments(content []byte) string {
	builder := strings.Builder{}
	builder.Grow(len(content))

	state := swiftStringScanState{}
	for index := 0; index < len(content); {
		if state.inString {
			index = consumeSwiftStringContent(content, index, &builder, &state)
			continue
		}
		index = consumeSwiftCodeContent(content, index, &builder, &state)
	}
	return builder.String()
}

func consumeSwiftStringContent(content []byte, index int, builder *strings.Builder, state *swiftStringScanState) int {
	if matchesSwiftStringDelimiter(content, index, state.rawHashCount, state.multiline) {
		delimiterLen := swiftStringDelimiterLength(state.rawHashCount, state.multiline)
		builder.WriteString(strings.Repeat(" ", delimiterLen))
		resetSwiftStringScanState(state)
		return index + delimiterLen
	}

	ch := content[index]
	if ch == '\n' {
		builder.WriteByte('\n')
		state.escaped = false
		return index + 1
	}
	if ch == '\\' && !state.multiline && state.rawHashCount == 0 && !state.escaped {
		state.escaped = true
		builder.WriteByte(' ')
		return index + 1
	}

	state.escaped = false
	builder.WriteByte(' ')
	return index + 1
}

func consumeSwiftCodeContent(content []byte, index int, builder *strings.Builder, state *swiftStringScanState) int {
	if startsSwiftLineComment(content, index) {
		return blankSwiftLineComment(content, index, builder)
	}

	hashCount, nextIndex, isMultiline, ok := detectSwiftStringStart(content, index)
	if ok {
		builder.WriteString(strings.Repeat(" ", nextIndex-index))
		state.inString = true
		state.multiline = isMultiline
		state.rawHashCount = hashCount
		state.escaped = false
		return nextIndex
	}

	builder.WriteByte(content[index])
	return index + 1
}

func resetSwiftStringScanState(state *swiftStringScanState) {
	state.inString = false
	state.multiline = false
	state.rawHashCount = 0
	state.escaped = false
}

func detectSwiftStringStart(content []byte, index int) (int, int, bool, bool) {
	cursor := index
	for cursor < len(content) && content[cursor] == '#' {
		cursor++
	}
	if cursor >= len(content) || content[cursor] != '"' {
		return 0, index, false, false
	}
	hashCount := cursor - index
	if cursor+2 < len(content) && content[cursor+1] == '"' && content[cursor+2] == '"' {
		return hashCount, cursor + 3, true, true
	}
	return hashCount, cursor + 1, false, true
}

func matchesSwiftStringDelimiter(content []byte, index int, rawHashCount int, multiline bool) bool {
	delimiterLen := swiftStringDelimiterLength(rawHashCount, multiline)
	if index+delimiterLen > len(content) {
		return false
	}
	quoteCount := 1
	if multiline {
		quoteCount = 3
	}
	for offset := 0; offset < quoteCount; offset++ {
		if content[index+offset] != '"' {
			return false
		}
	}
	for offset := 0; offset < rawHashCount; offset++ {
		if content[index+quoteCount+offset] != '#' {
			return false
		}
	}
	return true
}

func swiftStringDelimiterLength(rawHashCount int, multiline bool) int {
	quoteCount := 1
	if multiline {
		quoteCount = 3
	}
	return quoteCount + rawHashCount
}

func startsSwiftLineComment(content []byte, index int) bool {
	return index+1 < len(content) && content[index] == '/' && content[index+1] == '/'
}

func blankSwiftLineComment(content []byte, index int, builder *strings.Builder) int {
	for index < len(content) && content[index] != '\n' {
		builder.WriteByte(' ')
		index++
	}
	return index
}

func collectLocalDeclaredSymbols(content []byte) map[string]struct{} {
	localDeclaredSymbols := make(map[string]struct{})
	lines := swiftSymbolScanLines(content)
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || swiftImportPattern.MatchString(line) {
			continue
		}
		matches := swiftTypeDeclarationPattern.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			if len(match) != 2 {
				continue
			}
			key := lookupKey(match[1])
			if key == "" {
				continue
			}
			localDeclaredSymbols[key] = struct{}{}
		}
	}
	return localDeclaredSymbols
}
