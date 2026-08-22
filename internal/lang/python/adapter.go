package python

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("python", []string{"py"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Result, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	result := report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}

	scanResult, err := scanRepo(ctx, repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	analysisReq := req
	analysisReq.RepoPath = repoPath
	dependencies, warnings := buildRequestedPythonDependencies(analysisReq, scanResult)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	return result, nil
}

type importBinding = shared.ImportRecord

type fromImportSymbolLine struct {
	symbols string
	line    string
	index   int
}

type pendingFromImport struct {
	module      string
	symbolLines []fromImportSymbolLine
	parenDepth  int
}

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                []fileScan
	Warnings             []string
	DeclaredDependencies map[string]struct{}
	ImportedDependencies map[string]struct{}
}

func scanRepo(ctx context.Context, repoPath string) (scanResult, error) {
	result := scanResult{
		DeclaredDependencies: make(map[string]struct{}),
		ImportedDependencies: make(map[string]struct{}),
	}
	if repoPath == "" {
		return result, fmt.Errorf("repo path is empty")
	}
	declaredDependencies, warnings, err := collectDeclaredDependencies(ctx, repoPath)
	if err != nil {
		return result, err
	}
	result.DeclaredDependencies = declaredDependencies
	result.Warnings = append(result.Warnings, warnings...)

	err = filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return scanPythonRepoEntry(repoPath, path, entry, &result)
	})
	if err != nil {
		return result, err
	}
	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Python files found for analysis")
	}
	return result, nil
}

func scanPythonRepoEntry(repoPath string, path string, entry fs.DirEntry, result *scanResult) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	if !strings.HasSuffix(strings.ToLower(path), ".py") {
		return nil
	}
	cleanPath, err := enforceRepoBoundary(repoPath, path)
	if err != nil {
		return err
	}
	content, relativePath, err := readPythonFile(repoPath, cleanPath)
	if err != nil {
		return err
	}
	imports := parseImports(content, relativePath, repoPath)
	for _, imported := range imports {
		result.ImportedDependencies[imported.Dependency] = struct{}{}
	}
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Imports: imports,
		Usage:   shared.CountUsage(content, imports),
	})
	return nil
}

func enforceRepoBoundary(repoPath, path string) (string, error) {
	cleanRepo := filepath.Clean(repoPath)
	cleanPath := filepath.Clean(path)
	if strings.HasPrefix(cleanPath, cleanRepo+string(os.PathSeparator)) || cleanPath == cleanRepo {
		return cleanPath, nil
	}
	return "", fmt.Errorf("refusing to read path outside repo: %s", path)
}

func readPythonFile(repoPath, cleanPath string) ([]byte, string, error) {
	content, err := safeio.ReadFileUnder(repoPath, cleanPath)
	if err != nil {
		return nil, "", err
	}
	relativePath, err := filepath.Rel(repoPath, cleanPath)
	if err != nil {
		relativePath = cleanPath
	}
	return content, relativePath, nil
}

var (
	importLinePattern = regexp.MustCompile(`^\s*import\s+(.+)$`)
	fromLinePattern   = regexp.MustCompile(`^\s*from\s+([A-Za-z_][A-Za-z0-9_\.]*)\s+import\s+(.+)$`)
	pythonSkippedDirs = map[string]bool{
		"__pycache__":   true,
		".venv":         true,
		"venv":          true,
		".mypy_cache":   true,
		".pytest_cache": true,
	}
)

func parseImports(content []byte, filePath string, repoPath string) []importBinding {
	var pending *pendingFromImport
	var stringMask pythonStringMask

	return shared.ParseImportLines(content, filePath, func(line string, index int) []shared.ImportRecord {
		codeLine := stringMask.codeLine(line)
		lineNoComment := stripComment(codeLine)
		trimmed := strings.TrimSpace(lineNoComment)

		if records, handled := continuePendingFromImport(&pending, trimmed, lineNoComment, filePath, repoPath, index); handled {
			return records
		}

		return parseImportRecord(trimmed, lineNoComment, filePath, repoPath, index, &pending)
	})
}

type pythonStringMask struct {
	multilineQuote              string
	multilineFString            bool
	multilineReplacementFields  []pythonReplacementFieldState
	multilineReplacementStrings []pythonReplacementStringState
	shortFStringLineContinued   bool
	shortQuote                  byte
}

type pythonReplacementFieldState struct {
	formatSpec   bool
	bracketDepth int
	curlyDepth   int
}

type pythonReplacementStringState struct {
	delimiter         string
	fString           bool
	replacementFields []pythonReplacementFieldState
	lineContinued     bool
}

func (m *pythonStringMask) codeLine(line string) string {
	if m.resetShortStringOnBlankLine(line) {
		return line
	}
	if !strings.Contains(line, "'") && !strings.Contains(line, "\"") && m.multilineQuote == "" && m.shortQuote == 0 {
		return line
	}

	m.shortFStringLineContinued = false
	var builder strings.Builder
	builder.Grow(len(line))
	for index := 0; index < len(line); {
		if m.maskMultilineString(line, &index, &builder) {
			continue
		}
		if m.maskShortString(line, &index, &builder) {
			continue
		}

		current := line[index]
		if current == '#' {
			builder.WriteString(line[index:])
			break
		}
		if current != '\'' && current != '"' {
			builder.WriteByte(current)
			index++
			continue
		}

		if quote := line[index : index+1]; strings.HasPrefix(line[index:], quote+quote+quote) {
			m.startMultilineString(quote, line, &index, &builder)
			continue
		}

		m.startShortString(current, line, &index, &builder)
	}
	m.finishReplacementStringLine()
	m.finishShortFStringLine()
	return builder.String()
}

func (m *pythonStringMask) resetShortStringOnBlankLine(line string) bool {
	if line != "" {
		return false
	}
	m.shortQuote = 0
	if len(m.multilineQuote) == 1 && len(m.multilineReplacementStrings) == 0 {
		m.resetMultilineString()
	}
	return true
}

func (m *pythonStringMask) maskMultilineString(line string, index *int, builder *strings.Builder) bool {
	if m.multilineQuote == "" {
		return false
	}
	if m.maskMultilineFStringReplacementString(line, index, builder) {
		return true
	}
	if m.maskFStringBraceBackslash(line, index, builder) {
		return true
	}
	if m.maskActiveStringEscape(line, index, builder) {
		return true
	}
	if m.maskMultilineFStringReplacement(line, index, builder) {
		return true
	}
	if len(m.multilineReplacementFields) == 0 && strings.HasPrefix(line[*index:], m.multilineQuote) {
		m.closeMultilineString(index, builder)
		return true
	}
	builder.WriteByte(' ')
	*index++
	return true
}

func (m *pythonStringMask) maskShortString(line string, index *int, builder *strings.Builder) bool {
	if m.shortQuote == 0 {
		return false
	}
	next, closed, continued := maskPythonShortStringContent(line, *index, m.shortQuote, builder, false)
	*index = next
	if closed || !continued {
		m.shortQuote = 0
	}
	return true
}

func (m *pythonStringMask) maskMultilineFStringReplacement(line string, index *int, builder *strings.Builder) bool {
	if !m.multilineFString {
		return false
	}
	if len(m.multilineReplacementFields) == 0 {
		return startFStringReplacementField(line, index, builder, &m.multilineReplacementFields)
	}
	return m.maskFStringReplacementExpression(line, index, builder, &m.multilineReplacementFields)
}

func (m *pythonStringMask) maskMultilineFStringReplacementString(line string, index *int, builder *strings.Builder) bool {
	if len(m.multilineReplacementStrings) == 0 {
		return false
	}
	state := &m.multilineReplacementStrings[len(m.multilineReplacementStrings)-1]
	if !state.fString {
		return m.maskPlainReplacementString(line, index, builder, state)
	}
	return m.maskNestedFStringReplacementString(line, index, builder, state)
}

func (m *pythonStringMask) maskPlainReplacementString(line string, index *int, builder *strings.Builder, state *pythonReplacementStringState) bool {
	if len(state.delimiter) == 1 {
		next, closed, continued := maskPythonShortStringContent(line, *index, state.delimiter[0], builder, false)
		*index = next
		if closed || !continued {
			m.popReplacementString()
		}
		return true
	}
	if line[*index] == '\\' {
		builder.WriteByte(' ')
		*index++
		if *index < len(line) {
			builder.WriteByte(' ')
			*index++
		}
		return true
	}
	if strings.HasPrefix(line[*index:], state.delimiter) {
		writeSpaces(builder, len(state.delimiter))
		*index += len(state.delimiter)
		m.popReplacementString()
		return true
	}
	builder.WriteByte(' ')
	*index++
	return true
}

func (m *pythonStringMask) maskNestedFStringReplacementString(line string, index *int, builder *strings.Builder, state *pythonReplacementStringState) bool {
	if state.lineContinued {
		state.lineContinued = false
	}
	if len(state.replacementFields) == 0 {
		return m.maskNestedFStringText(line, index, builder, state)
	}
	return m.maskFStringReplacementExpression(line, index, builder, &state.replacementFields)
}

func (m *pythonStringMask) maskNestedFStringText(line string, index *int, builder *strings.Builder, state *pythonReplacementStringState) bool {
	if state.fString && line[*index] == '\\' && *index+1 < len(line) && line[*index+1] == '{' {
		maskByte(line, index, builder)
		return true
	}
	if maskReplacementStringEscape(line, index, builder, &state.lineContinued, len(state.delimiter) == 1) {
		return true
	}
	if strings.HasPrefix(line[*index:], state.delimiter) {
		writeSpaces(builder, len(state.delimiter))
		*index += len(state.delimiter)
		m.popReplacementString()
		return true
	}
	current := line[*index]
	if current == '{' {
		return startFStringReplacementField(line, index, builder, &state.replacementFields)
	}
	if current == '}' {
		maskByte(line, index, builder)
		consumeRepeatedByte('}', line, index, builder)
		return true
	}
	maskByte(line, index, builder)
	return true
}

func (m *pythonStringMask) maskFStringReplacementExpression(line string, index *int, builder *strings.Builder, fields *[]pythonReplacementFieldState) bool {
	current := line[*index]
	inFormatSpecText := isCurrentReplacementFieldFormatSpec(*fields)
	if !inFormatSpecText && (current == '\'' || current == '"') {
		m.startFStringReplacementString(line, index, builder)
		return true
	}
	if current == '#' && !inFormatSpecText {
		writeSpaces(builder, len(line)-*index)
		*index = len(line)
		return true
	}
	builder.WriteByte(' ')
	*index++
	updateFStringReplacementExpressionState(current, inFormatSpecText, fields)
	return true
}

func isCurrentReplacementFieldFormatSpec(fields []pythonReplacementFieldState) bool {
	return len(fields) > 0 && fields[len(fields)-1].formatSpec
}

func updateFStringReplacementExpressionState(current byte, inFormatSpecText bool, fields *[]pythonReplacementFieldState) {
	if len(*fields) == 0 {
		return
	}
	field := &(*fields)[len(*fields)-1]
	switch current {
	case '(', '[':
		if !inFormatSpecText {
			field.bracketDepth++
		}
	case ')', ']':
		if !inFormatSpecText && field.bracketDepth > 0 {
			field.bracketDepth--
		}
	case '{':
		if inFormatSpecText {
			*fields = append(*fields, pythonReplacementFieldState{})
			return
		}
		field.curlyDepth++
	case '}':
		if !inFormatSpecText && field.curlyDepth > 0 {
			field.curlyDepth--
			return
		}
		*fields = (*fields)[:len(*fields)-1]
	case ':':
		if !field.formatSpec && field.bracketDepth == 0 && field.curlyDepth == 0 {
			field.formatSpec = true
		}
	}
}

func (m *pythonStringMask) maskFStringBraceBackslash(line string, index *int, builder *strings.Builder) bool {
	if !m.multilineFString || line[*index] != '\\' || *index+1 >= len(line) || line[*index+1] != '{' {
		return false
	}
	maskByte(line, index, builder)
	return true
}

func startFStringReplacementField(line string, index *int, builder *strings.Builder, fields *[]pythonReplacementFieldState) bool {
	if line[*index] != '{' {
		return false
	}
	maskByte(line, index, builder)
	if consumeRepeatedByte('{', line, index, builder) {
		return true
	}
	*fields = append(*fields, pythonReplacementFieldState{})
	return true
}

func maskByte(line string, index *int, builder *strings.Builder) {
	builder.WriteByte(' ')
	*index++
}

func consumeRepeatedByte(value byte, line string, index *int, builder *strings.Builder) bool {
	if *index >= len(line) || line[*index] != value {
		return false
	}
	maskByte(line, index, builder)
	return true
}

func (m *pythonStringMask) startFStringReplacementString(line string, index *int, builder *strings.Builder) {
	delimiter := line[*index : *index+1]
	if strings.HasPrefix(line[*index:], delimiter+delimiter+delimiter) {
		delimiter += delimiter + delimiter
	}
	writeSpaces(builder, len(delimiter))
	*index += len(delimiter)
	m.multilineReplacementStrings = append(m.multilineReplacementStrings, pythonReplacementStringState{
		delimiter: delimiter,
		fString:   hasPythonFStringPrefix(line, *index-len(delimiter)),
	})
}

func (m *pythonStringMask) popReplacementString() {
	m.multilineReplacementStrings = m.multilineReplacementStrings[:len(m.multilineReplacementStrings)-1]
}

func (m *pythonStringMask) finishReplacementStringLine() {
	for len(m.multilineReplacementStrings) > 0 {
		state := &m.multilineReplacementStrings[len(m.multilineReplacementStrings)-1]
		if !state.fString || len(state.delimiter) != 1 || len(state.replacementFields) > 0 || state.lineContinued {
			return
		}
		m.popReplacementString()
	}
}

func (m *pythonStringMask) finishShortFStringLine() {
	if len(m.multilineQuote) != 1 || len(m.multilineReplacementFields) > 0 || len(m.multilineReplacementStrings) > 0 || m.shortFStringLineContinued {
		return
	}
	m.resetMultilineString()
}

func (m *pythonStringMask) startMultilineString(quote string, line string, index *int, builder *strings.Builder) {
	m.startFStringAwareString(quote+quote+quote, line, index, builder)
}

func (m *pythonStringMask) startFStringAwareString(delimiter string, line string, index *int, builder *strings.Builder) {
	m.multilineQuote = delimiter
	m.multilineFString = hasPythonFStringPrefix(line, *index)
	m.multilineReplacementFields = nil
	m.multilineReplacementStrings = nil
	m.shortFStringLineContinued = false
	writeSpaces(builder, len(m.multilineQuote))
	*index += len(m.multilineQuote)
}

func (m *pythonStringMask) startShortString(quote byte, line string, index *int, builder *strings.Builder) {
	if hasPythonFStringPrefix(line, *index) {
		m.startFStringAwareString(line[*index:*index+1], line, index, builder)
		return
	}
	next, closed, continued := maskPythonShortStringContent(line, *index, quote, builder, true)
	*index = next
	if !closed && continued {
		m.shortQuote = quote
	}
}

func (m *pythonStringMask) closeMultilineString(index *int, builder *strings.Builder) {
	writeSpaces(builder, len(m.multilineQuote))
	*index += len(m.multilineQuote)
	m.resetMultilineString()
}

func (m *pythonStringMask) resetMultilineString() {
	m.multilineQuote = ""
	m.multilineFString = false
	m.multilineReplacementFields = nil
	m.multilineReplacementStrings = nil
	m.shortFStringLineContinued = false
}

func (m *pythonStringMask) maskActiveStringEscape(line string, index *int, builder *strings.Builder) bool {
	if line[*index] != '\\' {
		return false
	}
	return maskReplacementStringEscape(line, index, builder, &m.shortFStringLineContinued, len(m.multilineQuote) == 1)
}

func maskReplacementStringEscape(line string, index *int, builder *strings.Builder, lineContinued *bool, retainContinuation bool) bool {
	if line[*index] != '\\' {
		return false
	}
	maskByte(line, index, builder)
	if *index >= len(line) {
		*lineContinued = retainContinuation
		return true
	}
	current := line[*index]
	maskByte(line, index, builder)
	if current == '\r' && *index == len(line) {
		*lineContinued = retainContinuation
	}
	return true
}

func maskPythonShortStringContent(line string, index int, quote byte, builder *strings.Builder, maskOpening bool) (int, bool, bool) {
	if maskOpening {
		builder.WriteByte(' ')
		index++
	}
	escaped := false
	for index < len(line) {
		current := line[index]
		builder.WriteByte(' ')
		index++
		if escaped {
			if current != '\r' || index != len(line) {
				escaped = false
			}
			continue
		}
		if current == '\\' {
			escaped = true
			continue
		}
		if current == quote {
			return index, true, false
		}
	}
	return index, false, escaped
}

func writeSpaces(builder *strings.Builder, count int) {
	for range count {
		builder.WriteByte(' ')
	}
}

func hasPythonFStringPrefix(line string, quoteIndex int) bool {
	if quoteIndex == 0 {
		return false
	}
	prefixStart := quoteIndex
	for prefixStart > 0 && strings.ContainsRune("rRuUbBfFtT", rune(line[prefixStart-1])) {
		prefixStart--
	}
	return strings.ContainsAny(line[prefixStart:quoteIndex], "fFtT")
}

func continuePendingFromImport(pending **pendingFromImport, trimmed string, lineNoComment string, filePath string, repoPath string, index int) ([]importBinding, bool) {
	if *pending == nil {
		return nil, false
	}
	if trimmed != "" {
		appendPendingFromImportLine(*pending, trimmed, lineNoComment, index)
	}
	if (*pending).parenDepth > 0 {
		return nil, true
	}
	records := parseFromImportLines((*pending).module, (*pending).symbolLines, filePath, repoPath)
	*pending = nil
	return records, true
}

func appendPendingFromImportLine(pending *pendingFromImport, symbols string, line string, index int) {
	pending.symbolLines = append(pending.symbolLines, fromImportSymbolLine{
		symbols: symbols,
		line:    line,
		index:   index,
	})
	pending.parenDepth += fromImportParenthesisDelta(symbols)
}

func parseImportRecord(trimmed string, lineNoComment string, filePath string, repoPath string, index int, pending **pendingFromImport) []importBinding {
	if trimmed == "" {
		return nil
	}
	if matches := importLinePattern.FindStringSubmatch(lineNoComment); len(matches) == 2 {
		return parseImportLine(matches[1], filePath, repoPath, index, lineNoComment)
	}
	return parseFromImportRecord(lineNoComment, filePath, repoPath, index, pending)
}

func parseFromImportRecord(lineNoComment string, filePath string, repoPath string, index int, pending **pendingFromImport) []importBinding {
	matches := fromLinePattern.FindStringSubmatch(lineNoComment)
	if len(matches) != 3 {
		return nil
	}
	symbols := strings.TrimSpace(matches[2])
	parenDepth := fromImportParenthesisDelta(symbols)
	if parenDepth > 0 {
		*pending = &pendingFromImport{
			module: matches[1],
			symbolLines: []fromImportSymbolLine{{
				symbols: symbols,
				line:    lineNoComment,
				index:   index,
			}},
			parenDepth: parenDepth,
		}
		return nil
	}
	return parseFromImportLine(matches[1], symbols, filePath, repoPath, index, lineNoComment)
}

func parseImportLine(partsText string, filePath string, repoPath string, index int, line string) []importBinding {
	bindings := make([]importBinding, 0)
	for _, part := range splitCSV(partsText) {
		moduleName, local := parseImportPart(part)
		if moduleName == "" {
			continue
		}
		dependency := dependencyFromModule(repoPath, moduleName)
		if dependency == "" {
			continue
		}
		if local == "" {
			local = strings.Split(moduleName, ".")[0]
		}
		bindings = append(bindings, importBinding{
			Dependency: dependency,
			Module:     moduleName,
			Name:       moduleName,
			Local:      local,
			Location:   importLocation(filePath, index, line),
		})
	}
	return bindings
}

func parseFromImportLine(moduleValue string, symbolsValue string, filePath string, repoPath string, index int, line string) []importBinding {
	moduleName, dependency, ok := resolveFromImportDependency(moduleValue, repoPath)
	if !ok {
		return nil
	}

	return parseFromImportSymbols(moduleName, dependency, symbolsValue, filePath, index, line)
}

func parseFromImportLines(moduleValue string, symbolLines []fromImportSymbolLine, filePath string, repoPath string) []importBinding {
	moduleName, dependency, ok := resolveFromImportDependency(moduleValue, repoPath)
	if !ok {
		return nil
	}

	bindings := make([]importBinding, 0)
	for _, symbolLine := range symbolLines {
		bindings = append(bindings, parseFromImportSymbols(moduleName, dependency, symbolLine.symbols, filePath, symbolLine.index, symbolLine.line)...)
	}
	return bindings
}

func resolveFromImportDependency(moduleValue, repoPath string) (string, string, bool) {
	moduleName := strings.TrimSpace(moduleValue)
	if strings.HasPrefix(moduleName, ".") {
		return "", "", false
	}
	dependency := dependencyFromModule(repoPath, moduleName)
	if dependency == "" {
		return "", "", false
	}
	return moduleName, dependency, true
}

func parseFromImportSymbols(moduleName string, dependency string, symbolsValue string, filePath string, index int, line string) []importBinding {
	symbolsValue = normalizeFromImportSymbols(symbolsValue)

	bindings := make([]importBinding, 0)
	for _, part := range splitCSV(symbolsValue) {
		part = strings.Trim(strings.TrimSpace(part), "()")
		symbol, local := parseImportPart(part)
		if symbol == "" {
			continue
		}
		if local == "" {
			local = symbol
		}
		bindings = append(bindings, importBinding{
			Dependency: dependency,
			Module:     moduleName,
			Name:       symbol,
			Local:      local,
			Wildcard:   symbol == "*",
			Location:   importLocation(filePath, index, line),
		})
	}
	return bindings
}

func fromImportParenthesisDelta(value string) int {
	return strings.Count(value, "(") - strings.Count(value, ")")
}

func normalizeFromImportSymbols(value string) string {
	value = strings.TrimSpace(value)
	for len(value) >= 2 && strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
		value = strings.TrimSpace(value[1 : len(value)-1])
	}
	return value
}

func importLocation(filePath string, index int, line string) report.Location {
	return shared.LocationFromLine(filePath, index, line)
}

func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		items = append(items, part)
	}
	return items
}

func parseImportPart(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if strings.Contains(value, " as ") {
		pieces := strings.SplitN(value, " as ", 2)
		moduleName := strings.TrimSpace(pieces[0])
		local := strings.TrimSpace(pieces[1])
		return moduleName, local
	}
	return value, ""
}

func stripComment(line string) string {
	return shared.StripLineComment(line, "#")
}

func dependencyFromModule(repoPath, moduleName string) string {
	moduleName = strings.TrimSpace(moduleName)
	if moduleName == "" {
		return ""
	}
	root := strings.Split(moduleName, ".")[0]
	if root == "" {
		return ""
	}
	if pythonStdlib[root] {
		return ""
	}
	if isLocalModule(repoPath, root) {
		return ""
	}
	return normalizeDependencyID(root)
}

func isLocalModule(repoPath, root string) bool {
	for _, searchRoot := range localModuleSearchRoots(repoPath) {
		// Use Lstat to avoid following symlinks that could escape the repo boundary.
		if info, err := os.Lstat(filepath.Join(searchRoot, root+".py")); err == nil && info.Mode()&os.ModeSymlink == 0 {
			return true
		}
		pkgDir := filepath.Join(searchRoot, root)
		if info, err := os.Lstat(pkgDir); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if marker, err := os.Lstat(filepath.Join(pkgDir, "__init__.py")); err == nil && marker.Mode()&os.ModeSymlink == 0 {
				return true
			}
		}
	}
	return false
}

func localModuleSearchRoots(repoPath string) []string {
	roots := []string{repoPath}

	srcRoot := filepath.Join(repoPath, "src")
	if info, err := os.Lstat(srcRoot); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		roots = append(roots, srcRoot)
	}

	return roots
}

func normalizeDependencyID(value string) string {
	normalized := report.CanonicalPackageNameForEcosystem("pypi", shared.NormalizeDependencyID(value))
	if canonical, ok := pythonKnownImportAliases[normalized]; ok {
		return canonical
	}
	return normalized
}

func shouldSkipDir(name string) bool {
	return ShouldSkipDirectory(name)
}

// ShouldSkipDirectory reports whether Python discovery ignores a directory.
func ShouldSkipDirectory(name string) bool {
	return shared.ShouldSkipDir(name, pythonSkippedDirs)
}

func pythonFileUsages(scan scanResult) []shared.FileUsage {
	return shared.MapFileUsages(scan.Files, func(file fileScan) []shared.ImportRecord { return file.Imports }, func(file fileScan) map[string]int { return file.Usage })
}

var pythonStdlib = map[string]bool{
	"abc": true, "argparse": true, "ast": true, "asyncio": true, "codecs": true, "collections": true,
	"concurrent": true, "contextlib": true, "copy": true, "csv": true, "dataclasses": true, "datetime": true,
	"decimal": true, "dis": true, "enum": true, "fractions": true, "functools": true, "gc": true,
	"glob": true, "hashlib": true, "http": true, "importlib": true, "inspect": true, "io": true,
	"itertools": true, "json": true, "keyword": true, "logging": true, "math": true, "multiprocessing": true,
	"operator": true, "os": true, "pathlib": true, "platform": true, "pprint": true, "queue": true,
	"random": true, "re": true, "shutil": true, "signal": true, "socket": true, "sqlite3": true,
	"ssl": true, "statistics": true, "string": true, "struct": true, "subprocess": true, "sys": true,
	"tempfile": true, "textwrap": true, "threading": true, "time": true, "traceback": true, "typing": true,
	"unittest": true, "urllib": true, "uuid": true, "warnings": true, "weakref": true, "xml": true, "zipfile": true,
}

var pythonKnownImportAliases = map[string]string{
	"bs4":      "beautifulsoup4",
	"cv2":      "opencv-python",
	"dateutil": "python-dateutil",
	"dotenv":   "python-dotenv",
	"pil":      "pillow",
	"sklearn":  "scikit-learn",
}
