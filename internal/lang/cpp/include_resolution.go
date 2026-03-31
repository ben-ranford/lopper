package cpp

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	maxScanFiles      = 4096
	maxWarningSamples = 5
)

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
	Catalog           dependencyCatalog
}

type includeResolver struct {
	repoPath    string
	includeDirs []string
	catalog     dependencyCatalog
}

type scanStage struct {
	scanner includeResolver
	result  scanResult
}

func scanRepo(ctx context.Context, repoPath string, compileInfo compileContext, catalog dependencyCatalog) (scanResult, error) {
	stage := scanStage{
		scanner: includeResolver{
			repoPath:    repoPath,
			includeDirs: compileInfo.IncludeDirs,
			catalog:     catalog,
		},
		result: scanResult{Catalog: catalog},
	}

	files, warnings, err := resolveScanFiles(ctx, repoPath, compileInfo)
	if err != nil {
		return stage.result, err
	}
	stage.result.Warnings = append(stage.result.Warnings, warnings...)
	if len(files) == 0 {
		stage.result.Warnings = append(stage.result.Warnings, "no C/C++ source files found for analysis")
		return stage.result, nil
	}

	for _, path := range files {
		if err := stage.process(ctx, path); err != nil {
			return stage.result, err
		}
	}
	stage.result.appendUnresolvedSummaryWarning()
	return stage.result, nil
}

func resolveScanFiles(ctx context.Context, repoPath string, compileInfo compileContext) ([]string, []string, error) {
	if len(compileInfo.SourceFiles) > 0 {
		files, warnings, err := filterCompileSourceHints(repoPath, compileInfo.SourceFiles)
		if err != nil {
			return nil, warnings, err
		}
		if len(files) > 0 {
			return files, warnings, nil
		}
		warnings = append(warnings, "compile database did not yield valid in-repo source files; falling back to repo scan")
		files, err = walkCPPFiles(ctx, repoPath)
		return files, warnings, err
	}
	files, err := walkCPPFiles(ctx, repoPath)
	return files, nil, err
}

func filterCompileSourceHints(repoPath string, sourceFiles []string) ([]string, []string, error) {
	files := make([]string, 0, len(sourceFiles))
	warnings := make([]string, 0)
	seen := make(map[string]struct{}, len(sourceFiles))

	for _, sourcePath := range sourceFiles {
		sourcePath = filepath.Clean(sourcePath)
		if !shared.IsPathWithin(repoPath, sourcePath) {
			warnings = append(warnings, fmt.Sprintf("skipping compile database file outside repo boundary: %s", sourcePath))
			continue
		}

		info, err := os.Stat(sourcePath)
		if err != nil {
			if os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("skipping compile database file missing from repo: %s", relOrBase(repoPath, sourcePath)))
				continue
			}
			return nil, warnings, err
		}
		if info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("skipping compile database path that is not a file: %s", relOrBase(repoPath, sourcePath)))
			continue
		}
		if _, ok := seen[sourcePath]; ok {
			continue
		}
		seen[sourcePath] = struct{}{}
		files = append(files, sourcePath)
	}

	sort.Strings(files)
	return files, warnings, nil
}

func (s *scanStage) process(ctx context.Context, path string) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	scanFile, unresolvedSamples, unresolvedCount, err := s.scanner.scanFile(path)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "path escapes root") {
			s.result.Warnings = append(s.result.Warnings, fmt.Sprintf("skipping compile database file outside repo boundary: %s", path))
			return nil
		}
		return err
	}
	if len(scanFile.Includes) > 0 {
		s.result.Files = append(s.result.Files, scanFile)
	}
	s.result.UnresolvedCount += unresolvedCount
	s.result.appendSampleWarnings(unresolvedSamples)
	return nil
}

func (r *scanResult) appendSampleWarnings(samples []string) {
	for _, sample := range samples {
		if len(r.UnresolvedSamples) >= maxWarningSamples {
			return
		}
		r.UnresolvedSamples = append(r.UnresolvedSamples, sample)
	}
}

func (r *scanResult) appendUnresolvedSummaryWarning() {
	if r.UnresolvedCount == 0 {
		return
	}
	message := fmt.Sprintf("cpp include mapping unresolved for %d directive(s)", r.UnresolvedCount)
	if len(r.UnresolvedSamples) > 0 {
		message += ": " + strings.Join(r.UnresolvedSamples, ", ")
	}
	r.Warnings = append(r.Warnings, message)
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

func (r *includeResolver) scanFile(path string) (fileScan, []string, int, error) {
	scan := fileScan{}
	content, err := safeio.ReadFileUnder(r.repoPath, path)
	if err != nil {
		return scan, nil, 0, err
	}

	relative, err := filepath.Rel(r.repoPath, path)
	if err != nil {
		relative = path
	}
	scan.Path = relative

	parsed := parseIncludes(content)
	unresolvedSamples := make([]string, 0)
	unresolvedCount := 0
	for _, include := range parsed {
		dependency, unresolved := r.mapIncludeToDependency(path, include)
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

func mapIncludeToDependency(repoPath string, sourcePath string, include parsedInclude, includeDirs []string, catalog dependencyCatalog) (string, bool) {
	resolver := &includeResolver{
		repoPath:    repoPath,
		includeDirs: includeDirs,
		catalog:     catalog,
	}
	return resolver.mapIncludeToDependency(sourcePath, include)
}

func (r *includeResolver) mapIncludeToDependency(sourcePath string, include parsedInclude) (string, bool) {
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
	if r.includeResolvesWithinRepo(sourcePath, header) {
		return "", false
	}
	if include.Delimiter == '"' {
		return "", true
	}

	dependency := dependencyFromIncludePath(header)
	if dependency == "" {
		return "", true
	}
	return correlateDeclaredDependency(dependency, r.catalog), false
}

func (r *includeResolver) includeResolvesWithinRepo(sourcePath string, header string) bool {
	sourceDir := filepath.Dir(sourcePath)
	candidates := []string{filepath.Join(sourceDir, filepath.FromSlash(header))}
	for _, includeDir := range r.includeDirs {
		candidates = append(candidates, filepath.Join(includeDir, filepath.FromSlash(header)))
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if shared.IsPathWithin(r.repoPath, candidate) {
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
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '+' {
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

var cppStdHeaderSet = map[string]struct{}{
	"algorithm": {}, "array": {}, "atomic": {}, "bitset": {}, "cassert": {}, "cctype": {}, "cerrno": {}, "cfenv": {}, "cfloat": {}, "charconv": {}, "chrono": {}, "cinttypes": {}, "ciso646": {}, "climits": {}, "clocale": {}, "cmath": {}, "codecvt": {}, "compare": {}, "complex": {}, "condition_variable": {}, "coroutine": {}, "csetjmp": {}, "csignal": {}, "cstdarg": {}, "cstddef": {}, "cstdint": {}, "cstdio": {}, "cstdlib": {}, "cstring": {}, "ctime": {}, "cuchar": {}, "cwchar": {}, "cwctype": {}, "deque": {}, "exception": {}, "execution": {}, "filesystem": {}, "forward_list": {}, "fstream": {}, "functional": {}, "future": {}, "initializer_list": {}, "iomanip": {}, "ios": {}, "iosfwd": {}, "iostream": {}, "istream": {}, "iterator": {}, "latch": {}, "limits": {}, "list": {}, "locale": {}, "map": {}, "memory": {}, "memory_resource": {}, "mutex": {}, "new": {}, "numbers": {}, "numeric": {}, "optional": {}, "ostream": {}, "queue": {}, "random": {}, "ranges": {}, "ratio": {}, "regex": {}, "scoped_allocator": {}, "semaphore": {}, "set": {}, "shared_mutex": {}, "source_location": {}, "span": {}, "sstream": {}, "stack": {}, "stdexcept": {}, "stop_token": {}, "streambuf": {}, "string": {}, "string_view": {}, "strstream": {}, "syncstream": {}, "system_error": {}, "thread": {}, "tuple": {}, "type_traits": {}, "typeindex": {}, "typeinfo": {}, "unordered_map": {}, "unordered_set": {}, "utility": {}, "valarray": {}, "variant": {}, "vector": {},
	"assert": {}, "ctype": {}, "errno": {}, "float": {}, "inttypes": {}, "math": {}, "setjmp": {}, "signal": {}, "stdarg": {}, "stddef": {}, "stdint": {}, "stdio": {}, "stdlib": {}, "time": {}, "wchar": {}, "wctype": {},
}
