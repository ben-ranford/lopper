package cpp

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

const (
	compileCommandsFile = "compile_commands.json"
	cmakeListsFile      = "CMakeLists.txt"
	includeFlag         = "-I"
	isystemFlag         = "-isystem"
	iquoteFlag          = "-iquote"
	maxDetectFiles      = 2048
	maxScanFiles        = 4096
	maxCompileDatabases = 64
	maxWarningSamples   = 5
)

type compileCommandEntry struct {
	Directory string   `json:"directory"`
	Command   string   `json:"command"`
	File      string   `json:"file"`
	Arguments []string `json:"arguments"`
}

type compileContext struct {
	HasCompileDatabase bool
	IncludeDirs        []string
	SourceFiles        []string
	Warnings           []string
}

type parsedInclude struct {
	Path      string
	Delimiter byte
	Line      int
	Column    int
}

type includeRecord struct {
	Dependency string
	Header     string
	Location   report.Location
}

type fileScan struct {
	Path     string
	Includes []includeRecord
}

type scanResult struct {
	Files             []fileScan
	Warnings          []string
	UnresolvedCount   int
	UnresolvedSamples []string
}

func (a *Adapter) Detect(ctx context.Context, repoPath string) (bool, error) {
	return shared.DetectMatched(ctx, repoPath, a.DetectWithConfidence)
}

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := shared.ApplyRootSignals(repoPath, cppRootSignals, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	err := shared.WalkRepoFiles(ctx, repoPath, maxDetectFiles, shared.ShouldSkipCommonDir, func(path string, entry fs.DirEntry) error {
		updateDetection(path, &detection, roots)
		return nil
	})
	if err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func updateDetection(path string, detection *language.Detection, roots map[string]struct{}) {
	switch filepath.Base(path) {
	case compileCommandsFile:
		markDetection(detection, roots, filepath.Dir(path), 20)
	case cmakeListsFile:
		markDetection(detection, roots, filepath.Dir(path), 12)
	case "Makefile", "makefile", "GNUmakefile":
		markDetection(detection, roots, filepath.Dir(path), 10)
	}

	if isCPPSourceOrHeader(path) {
		markDetection(detection, roots, "", 2)
	}
}

func markDetection(detection *language.Detection, roots map[string]struct{}, root string, confidence int) {
	detection.Matched = true
	detection.Confidence += confidence
	if root != "" {
		roots[root] = struct{}{}
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

	compileInfo, err := loadCompileContext(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, compileInfo.Warnings...)

	scan, err := scanRepo(ctx, repoPath, compileInfo)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, warnings := buildRequestedCPPDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}

func loadCompileContext(repoPath string) (compileContext, error) {
	result := compileContext{}
	if repoPath == "" {
		return result, fmt.Errorf("repo path is empty")
	}

	includeDirSet := make(map[string]struct{})
	sourceFileSet := make(map[string]struct{})
	visited := 0

	err := shared.WalkRepoFiles(nil, repoPath, 0, shared.ShouldSkipCommonDir, func(path string, entry fs.DirEntry) error {
		if filepath.Base(path) != compileCommandsFile {
			return nil
		}
		visited++
		if visited > maxCompileDatabases {
			return fs.SkipAll
		}
		warnings, err := collectCompileDatabase(path, repoPath, includeDirSet, sourceFileSet)
		result.Warnings = append(result.Warnings, warnings...)
		if err != nil {
			return err
		}
		result.HasCompileDatabase = true
		return nil
	})
	if err != nil {
		return result, err
	}

	if !result.HasCompileDatabase {
		result.Warnings = append(result.Warnings, "compile_commands.json not found; using include-graph heuristics without translation unit context")
	}

	result.IncludeDirs = shared.SortedKeys(includeDirSet)
	result.SourceFiles = shared.SortedKeys(sourceFileSet)
	return result, nil
}

func collectCompileDatabase(path string, repoPath string, includeDirSet map[string]struct{}, sourceFileSet map[string]struct{}) ([]string, error) {
	warnings := make([]string, 0)
	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return nil, err
	}
	var entries []compileCommandEntry
	if err := json.Unmarshal(content, &entries); err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to parse %s: %v", relOrBase(repoPath, path), err))
		return warnings, nil
	}

	for _, entry := range entries {
		baseDir := resolveCompileDirectory(path, entry.Directory)
		sourcePath := resolveCompilePath(baseDir, entry.File)
		if sourcePath != "" && isCPPSourceFile(sourcePath) {
			sourceFileSet[sourcePath] = struct{}{}
		}
		args := entry.Arguments
		if len(args) == 0 && entry.Command != "" {
			args = strings.Fields(entry.Command)
		}
		for _, includeDir := range extractIncludeDirs(args, baseDir) {
			if includeDir != "" {
				includeDirSet[includeDir] = struct{}{}
			}
		}
	}
	return warnings, nil
}

func resolveCompileDirectory(dbPath, directory string) string {
	base := filepath.Dir(dbPath)
	if strings.TrimSpace(directory) == "" {
		return base
	}
	return resolveCompilePath(base, directory)
}

func resolveCompilePath(base, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}

func extractIncludeDirs(args []string, baseDir string) []string {
	seen := make(map[string]struct{})
	items := make([]string, 0)
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			continue
		}
		switch {
		case arg == includeFlag || arg == isystemFlag || arg == iquoteFlag:
			if i+1 >= len(args) {
				continue
			}
			i++
			addIncludeDir(resolveCompilePath(baseDir, args[i]), seen, &items)
		case strings.HasPrefix(arg, includeFlag) && len(arg) > len(includeFlag):
			addIncludeDir(resolveCompilePath(baseDir, arg[len(includeFlag):]), seen, &items)
		case strings.HasPrefix(arg, isystemFlag) && len(arg) > len(isystemFlag):
			addIncludeDir(resolveCompilePath(baseDir, arg[len(isystemFlag):]), seen, &items)
		case strings.HasPrefix(arg, iquoteFlag) && len(arg) > len(iquoteFlag):
			addIncludeDir(resolveCompilePath(baseDir, arg[len(iquoteFlag):]), seen, &items)
		}
	}
	sort.Strings(items)
	return items
}

func addIncludeDir(path string, seen map[string]struct{}, items *[]string) {
	if path == "" {
		return
	}
	path = filepath.Clean(path)
	if _, ok := seen[path]; ok {
		return
	}
	seen[path] = struct{}{}
	*items = append(*items, path)
}

func scanRepo(ctx context.Context, repoPath string, compileInfo compileContext) (scanResult, error) {
	result := scanResult{}
	files, err := resolveScanFiles(ctx, repoPath, compileInfo)
	if err != nil {
		return result, err
	}

	if len(files) == 0 {
		result.Warnings = append(result.Warnings, "no C/C++ source files found for analysis")
		return result, nil
	}

	for _, path := range files {
		if err := processScanFile(ctx, repoPath, path, compileInfo.IncludeDirs, &result); err != nil {
			return result, err
		}
	}

	appendUnresolvedSummaryWarning(&result)
	return result, nil
}

func resolveScanFiles(ctx context.Context, repoPath string, compileInfo compileContext) ([]string, error) {
	if len(compileInfo.SourceFiles) > 0 {
		return compileInfo.SourceFiles, nil
	}
	return walkCPPFiles(ctx, repoPath)
}

func processScanFile(ctx context.Context, repoPath string, path string, includeDirs []string, result *scanResult) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	scanFile, unresolvedSamples, unresolvedCount, err := scanCPPFile(repoPath, path, includeDirs)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "path escapes root") {
			result.Warnings = append(result.Warnings, fmt.Sprintf("skipping compile database file outside repo boundary: %s", path))
			return nil
		}
		return err
	}
	if len(scanFile.Includes) > 0 {
		result.Files = append(result.Files, scanFile)
	}
	result.UnresolvedCount += unresolvedCount
	appendSampleWarnings(result, unresolvedSamples)
	return nil
}

func appendSampleWarnings(result *scanResult, samples []string) {
	for _, sample := range samples {
		if len(result.UnresolvedSamples) >= maxWarningSamples {
			return
		}
		result.UnresolvedSamples = append(result.UnresolvedSamples, sample)
	}
}

func appendUnresolvedSummaryWarning(result *scanResult) {
	if result.UnresolvedCount == 0 {
		return
	}
	message := fmt.Sprintf("cpp include mapping unresolved for %d directive(s)", result.UnresolvedCount)
	if len(result.UnresolvedSamples) > 0 {
		message += ": " + strings.Join(result.UnresolvedSamples, ", ")
	}
	result.Warnings = append(result.Warnings, message)
}

func walkCPPFiles(ctx context.Context, repoPath string) ([]string, error) {
	files := make([]string, 0)
	err := shared.WalkRepoFiles(ctx, repoPath, maxScanFiles, shared.ShouldSkipCommonDir, func(path string, entry fs.DirEntry) error {
		if isCPPSourceFile(path) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func scanCPPFile(repoPath string, path string, includeDirs []string) (fileScan, []string, int, error) {
	scan := fileScan{}
	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return scan, nil, 0, err
	}

	relative, err := filepath.Rel(repoPath, path)
	if err != nil {
		relative = path
	}
	scan.Path = relative

	parsed := parseIncludes(content)
	unresolvedSamples := make([]string, 0)
	unresolvedCount := 0
	for _, include := range parsed {
		dependency, unresolved := mapIncludeToDependency(repoPath, path, include, includeDirs)
		if dependency == "" {
			if unresolved {
				unresolvedCount++
				if len(unresolvedSamples) < maxWarningSamples {
					unresolvedSamples = append(unresolvedSamples, fmt.Sprintf("%s:%d:%s", relative, include.Line, include.Path))
				}
			}
			continue
		}
		scan.Includes = append(scan.Includes, includeRecord{
			Dependency: dependency,
			Header:     include.Path,
			Location: report.Location{
				File:   relative,
				Line:   include.Line,
				Column: include.Column,
			},
		})
	}

	return scan, unresolvedSamples, unresolvedCount, nil
}

func parseIncludes(content []byte) []parsedInclude {
	lines := strings.Split(string(content), "\n")
	includes := make([]parsedInclude, 0)
	for idx, line := range lines {
		include, ok := parseIncludeLine(line, idx+1)
		if !ok {
			continue
		}
		includes = append(includes, include)
	}
	return includes
}

func parseIncludeLine(line string, lineNo int) (parsedInclude, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmed, "#") {
		return parsedInclude{}, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
	if !strings.HasPrefix(rest, "include") {
		return parsedInclude{}, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(rest, "include"))
	if payload == "" {
		return parsedInclude{}, false
	}
	delimiter := payload[0]
	if delimiter != '<' && delimiter != '"' {
		return makeParsedInclude(payload, delimiter, line, lineNo), true
	}
	header, ok := extractDelimitedHeader(payload, delimiter)
	if !ok {
		return makeParsedInclude(payload, delimiter, line, lineNo), true
	}
	if header == "" {
		return parsedInclude{}, false
	}
	return makeParsedInclude(filepath.ToSlash(header), delimiter, line, lineNo), true
}

func extractDelimitedHeader(payload string, delimiter byte) (string, bool) {
	closing := byte('>')
	if delimiter == '"' {
		closing = '"'
	}
	end := strings.IndexByte(payload[1:], closing)
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(payload[1 : 1+end]), true
}

func makeParsedInclude(path string, delimiter byte, line string, lineNo int) parsedInclude {
	return parsedInclude{
		Path:      path,
		Delimiter: delimiter,
		Line:      lineNo,
		Column:    shared.FirstContentColumn(line),
	}
}

func mapIncludeToDependency(repoPath string, sourcePath string, include parsedInclude, includeDirs []string) (string, bool) {
	header := strings.TrimSpace(include.Path)
	if header == "" {
		return "", true
	}
	if include.Delimiter != '<' && include.Delimiter != '"' {
		return "", true
	}
	if isLikelyStdHeader(header) {
		return "", false
	}
	if includeResolvesWithinRepo(repoPath, sourcePath, header, includeDirs) {
		return "", false
	}
	if include.Delimiter == '"' {
		return "", true
	}

	dependency := dependencyFromIncludePath(header)
	if dependency == "" {
		return "", true
	}
	return dependency, false
}

func includeResolvesWithinRepo(repoPath string, sourcePath string, header string, includeDirs []string) bool {
	sourceDir := filepath.Dir(sourcePath)
	candidates := []string{filepath.Join(sourceDir, filepath.FromSlash(header))}
	for _, includeDir := range includeDirs {
		candidates = append(candidates, filepath.Join(includeDir, filepath.FromSlash(header)))
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if shared.IsPathWithin(repoPath, candidate) {
			return true
		}
	}
	return false
}

func dependencyFromIncludePath(header string) string {
	header = strings.TrimSpace(filepath.ToSlash(header))
	header = strings.TrimPrefix(header, "./")
	header = strings.TrimPrefix(header, "../")
	if header == "" {
		return ""
	}
	parts := strings.Split(header, "/")
	token := parts[0]
	if token == "" || token == "." || token == ".." {
		return ""
	}
	if strings.Contains(token, ".") {
		ext := filepath.Ext(token)
		if ext != "" {
			token = strings.TrimSuffix(token, ext)
		}
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	for _, r := range token {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '+') {
			return ""
		}
	}
	return strings.ToLower(token)
}

func isLikelyStdHeader(header string) bool {
	header = strings.TrimSpace(filepath.ToSlash(header))
	if header == "" {
		return false
	}
	if strings.HasPrefix(header, "sys/") || strings.HasPrefix(header, "bits/") || strings.HasPrefix(header, "linux/") {
		return true
	}

	base := strings.TrimSpace(filepath.Base(header))
	if base == "" {
		return false
	}
	base = strings.TrimSuffix(base, filepath.Ext(base))
	_, ok := cppStdHeaderSet[strings.ToLower(base)]
	return ok
}

func buildRequestedCPPDependencies(req language.Request, scan scanResult) ([]report.DependencyReport, []string) {
	weights := resolveRemovalCandidateWeights(req.RemovalCandidateWeights)
	switch {
	case req.Dependency != "":
		dependency := shared.NormalizeDependencyID(req.Dependency)
		dep, warnings := buildDependencyReport(dependency, scan)
		return []report.DependencyReport{dep}, warnings
	case req.TopN > 0:
		return buildTopCPPDependencies(req.TopN, scan, weights)
	default:
		return nil, []string{"no dependency or top-N target provided"}
	}
}

func buildTopCPPDependencies(topN int, scan scanResult, weights report.RemovalCandidateWeights) ([]report.DependencyReport, []string) {
	dependencySet := make(map[string]struct{})
	for _, file := range scan.Files {
		for _, include := range file.Includes {
			if include.Dependency != "" {
				dependencySet[shared.NormalizeDependencyID(include.Dependency)] = struct{}{}
			}
		}
	}
	dependencies := shared.SortedKeys(dependencySet)
	reportBuilder := func(dependency string) (report.DependencyReport, []string) {
		return buildDependencyReport(dependency, scan)
	}
	return shared.BuildTopReports(topN, dependencies, reportBuilder, weights)
}

func resolveRemovalCandidateWeights(value *report.RemovalCandidateWeights) report.RemovalCandidateWeights {
	if value == nil {
		return report.DefaultRemovalCandidateWeights()
	}
	return report.NormalizeRemovalCandidateWeights(*value)
}

func buildDependencyReport(dependency string, scan scanResult) (report.DependencyReport, []string) {
	reportData := report.DependencyReport{Name: dependency, Language: "cpp"}

	usedByHeader := make(map[string]int)
	usedImportsByHeader := make(map[string]*report.ImportUse)
	for _, file := range scan.Files {
		for _, include := range file.Includes {
			if shared.NormalizeDependencyID(include.Dependency) != dependency {
				continue
			}
			usedByHeader[include.Header]++
			entry, ok := usedImportsByHeader[include.Header]
			if !ok {
				entry = &report.ImportUse{Name: include.Header, Module: include.Header}
				usedImportsByHeader[include.Header] = entry
			}
			entry.Locations = append(entry.Locations, include.Location)
		}
	}

	headers := sortedCountKeys(usedByHeader)
	reportData.TotalExportsCount = len(headers)
	reportData.UsedExportsCount = len(headers)
	if reportData.TotalExportsCount > 0 {
		reportData.UsedPercent = 100
	}
	reportData.TopUsedSymbols = buildTopUsedSymbols(usedByHeader)
	reportData.UsedImports = flattenImportUses(usedImportsByHeader, headers)

	if reportData.TotalExportsCount == 0 {
		return reportData, []string{fmt.Sprintf("no mapped include usage found for dependency %s", dependency)}
	}
	return reportData, nil
}

func buildTopUsedSymbols(usage map[string]int) []report.SymbolUsage {
	symbols := make([]report.SymbolUsage, 0, len(usage))
	for name, count := range usage {
		symbols = append(symbols, report.SymbolUsage{Name: name, Module: name, Count: count})
	}
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Count == symbols[j].Count {
			return symbols[i].Name < symbols[j].Name
		}
		return symbols[i].Count > symbols[j].Count
	})
	if len(symbols) > 5 {
		symbols = symbols[:5]
	}
	return symbols
}

func flattenImportUses(imports map[string]*report.ImportUse, orderedKeys []string) []report.ImportUse {
	items := make([]report.ImportUse, 0, len(imports))
	for _, key := range orderedKeys {
		if current, ok := imports[key]; ok {
			items = append(items, *current)
		}
	}
	return items
}

func sortedCountKeys(values map[string]int) []string {
	items := make([]string, 0, len(values))
	for name := range values {
		items = append(items, name)
	}
	sort.Strings(items)
	return items
}

func relOrBase(repoPath, value string) string {
	if rel, err := filepath.Rel(repoPath, value); err == nil {
		return rel
	}
	return filepath.Base(value)
}

func isCPPSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".c", ".cc", ".cpp", ".cxx", ".c++":
		return true
	default:
		return false
	}
}

func isCPPSourceOrHeader(path string) bool {
	if isCPPSourceFile(path) {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".h", ".hh", ".hpp", ".hxx", ".h++":
		return true
	default:
		return false
	}
}

var cppRootSignals = []shared.RootSignal{
	{Name: compileCommandsFile, Confidence: 60},
	{Name: cmakeListsFile, Confidence: 45},
	{Name: "Makefile", Confidence: 35},
	{Name: "makefile", Confidence: 35},
	{Name: "GNUmakefile", Confidence: 35},
}

var cppStdHeaderSet = map[string]struct{}{
	"algorithm": {}, "array": {}, "atomic": {}, "bitset": {}, "cassert": {}, "cctype": {}, "cerrno": {}, "cfenv": {}, "cfloat": {}, "charconv": {}, "chrono": {}, "cinttypes": {}, "ciso646": {}, "climits": {}, "clocale": {}, "cmath": {}, "codecvt": {}, "compare": {}, "complex": {}, "condition_variable": {}, "coroutine": {}, "csetjmp": {}, "csignal": {}, "cstdarg": {}, "cstddef": {}, "cstdint": {}, "cstdio": {}, "cstdlib": {}, "cstring": {}, "ctime": {}, "cuchar": {}, "cwchar": {}, "cwctype": {}, "deque": {}, "exception": {}, "execution": {}, "filesystem": {}, "forward_list": {}, "fstream": {}, "functional": {}, "future": {}, "initializer_list": {}, "iomanip": {}, "ios": {}, "iosfwd": {}, "iostream": {}, "istream": {}, "iterator": {}, "latch": {}, "limits": {}, "list": {}, "locale": {}, "map": {}, "memory": {}, "memory_resource": {}, "mutex": {}, "new": {}, "numbers": {}, "numeric": {}, "optional": {}, "ostream": {}, "queue": {}, "random": {}, "ranges": {}, "ratio": {}, "regex": {}, "scoped_allocator": {}, "semaphore": {}, "set": {}, "shared_mutex": {}, "source_location": {}, "span": {}, "sstream": {}, "stack": {}, "stdexcept": {}, "stop_token": {}, "streambuf": {}, "string": {}, "string_view": {}, "strstream": {}, "syncstream": {}, "system_error": {}, "thread": {}, "tuple": {}, "type_traits": {}, "typeindex": {}, "typeinfo": {}, "unordered_map": {}, "unordered_set": {}, "utility": {}, "valarray": {}, "variant": {}, "vector": {},
	"assert": {}, "ctype": {}, "errno": {}, "float": {}, "inttypes": {}, "math": {}, "setjmp": {}, "signal": {}, "stdarg": {}, "stddef": {}, "stdint": {}, "stdio": {}, "stdlib": {}, "time": {}, "wchar": {}, "wctype": {},
}
