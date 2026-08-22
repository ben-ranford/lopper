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
	SkippedLargeFiles int
	Catalog           dependencyCatalog
}

type includeResolver struct {
	repoPath    string
	includeDirs []string
	catalog     dependencyCatalog
}

type includeLookup struct {
	sourcePath string
	header     string
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
	stage.result.appendLargeFileWarning()
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
		if shared.IsPureSentinelError(err, safeio.ErrFileTooLarge) {
			s.result.SkippedLargeFiles++
			return nil
		}
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

func (r *scanResult) appendLargeFileWarning() {
	if r.SkippedLargeFiles == 0 {
		return
	}
	r.Warnings = append(r.Warnings, fmt.Sprintf("skipped %d large C/C++ source file(s) above %d bytes", r.SkippedLargeFiles, maxScannableCPPFile))
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
	content, err := safeio.ReadFileUnderLimit(r.repoPath, path, maxScannableCPPFile)
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
	directive, payload := splitPreprocessorDirective(rest)
	if directive != "include" {
		return parsedInclude{}, false
	}
	payload = strings.TrimSpace(payload)
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

func splitPreprocessorDirective(value string) (string, string) {
	end := 0
	for end < len(value) {
		ch := value[end]
		isAlphaOrUnderscore := ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
		if isAlphaOrUnderscore || (end > 0 && ch >= '0' && ch <= '9') {
			end++
			continue
		}
		break
	}
	return value[:end], value[end:]
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

func mapIncludeToDependency(repoPath, sourcePath string, include parsedInclude, includeDirs []string, catalog dependencyCatalog) (string, bool) {
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
	if r.includeResolvesWithinRepo(includeLookup{
		sourcePath: sourcePath,
		header:     header,
	}) {
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

func (r *includeResolver) includeResolvesWithinRepo(include includeLookup) bool {
	sourceDir := filepath.Dir(include.sourcePath)
	candidates := []string{filepath.Join(sourceDir, filepath.FromSlash(include.header))}
	for _, includeDir := range r.includeDirs {
		candidates = append(candidates, filepath.Join(includeDir, filepath.FromSlash(include.header)))
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
	if strings.Contains(header, "/") {
		if hasOSCompilerHeaderPrefix(header) {
			return true
		}
		return isKnownCompilerQualifiedStdHeader(header)
	}

	base := strings.TrimSpace(filepath.Base(header))
	if base == "" {
		return false
	}
	base = strings.TrimSuffix(base, filepath.Ext(base))
	_, ok := cppStdHeaderSet[strings.ToLower(base)]
	return ok
}

func hasOSCompilerHeaderPrefix(header string) bool {
	for _, prefix := range []string{"sys/", "bits/", "linux/", "asm/", "asm-generic/"} {
		if strings.HasPrefix(header, prefix) {
			return true
		}
	}
	return false
}

func isKnownCompilerQualifiedStdHeader(header string) bool {
	header = strings.ToLower(strings.TrimSpace(filepath.ToSlash(header)))
	if !hasCompilerQualifiedStdHeaderPrefix(header) {
		return false
	}
	if isKnownCompilerQualifiedStdHeaderPath(header) {
		return true
	}
	base := header[strings.LastIndex(header, "/")+1:]
	if base == "" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(base))
	stem := strings.TrimSuffix(base, ext)
	if stem == "" {
		return false
	}
	switch ext {
	case "":
		return isKnownCompilerQualifiedStdHeaderStem(header, stem)
	case ".h":
		return isKnownCompilerQualifiedStdHHeader(header, stem)
	case ".hpp":
		return isKnownCompilerQualifiedStdHPPHeader(header, stem)
	default:
		return false
	}
}

func isKnownCompilerQualifiedStdHeaderPath(header string) bool {
	_, ok := cppQualifiedStdHeaderWithExtensionSet[strings.ToLower(strings.TrimSpace(header))]
	return ok
}

func isKnownCompilerQualifiedStdHeaderStem(header, stem string) bool {
	if _, ok := cppStdHeaderSet[stem]; ok {
		return true
	}
	if strings.HasPrefix(header, "backward/") {
		_, ok := cppBackwardQualifiedStdHeaderStemSet[stem]
		return ok
	}
	return false
}

func isKnownCompilerQualifiedStdHHeader(header, stem string) bool {
	if strings.HasPrefix(header, "parallel/") {
		_, ok := cppParallelQualifiedStdHeaderStemSet[stem]
		return ok
	}
	return false
}

func isKnownCompilerQualifiedStdHPPHeader(header, stem string) bool {
	if !strings.HasPrefix(header, "ext/pb_ds/") {
		return false
	}
	if _, ok := cppStdHeaderSet[stem]; ok {
		return true
	}
	_, ok := cppExtPBDSQualifiedStdHeaderStemSet[stem]
	return ok
}

func hasCompilerQualifiedStdHeaderPrefix(header string) bool {
	for _, prefix := range []string{"backward/", "debug/", "experimental/", "ext/", "parallel/", "tr1/", "tr2/"} {
		if strings.HasPrefix(header, prefix) {
			return true
		}
	}
	return false
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

var cppQualifiedStdHeaderWithExtensionSet = map[string]struct{}{
	"backward/auto_ptr.h":           {},
	"backward/backward_warning.h":   {},
	"backward/binders.h":            {},
	"backward/hash_fun.h":           {},
	"backward/hashtable.h":          {},
	"debug/assertions.h":            {},
	"debug/debug.h":                 {},
	"debug/functions.h":             {},
	"debug/macros.h":                {},
	"debug/map.h":                   {},
	"debug/safe_base.h":             {},
	"debug/safe_iterator.h":         {},
	"debug/set.h":                   {},
	"ext/aligned_buffer.h":          {},
	"ext/alloc_traits.h":            {},
	"ext/atomicity.h":               {},
	"ext/numeric_traits.h":          {},
	"ext/pb_ds/assoc_container.hpp": {},
	"ext/pb_ds/priority_queue.hpp":  {},
	"ext/pb_ds/tag_and_trait.hpp":   {},
	"ext/pb_ds/tree_policy.hpp":     {},
	"ext/type_traits.h":             {},
	"parallel/base.h":               {},
	"parallel/basic_iterator.h":     {},
	"parallel/compatibility.h":      {},
	"parallel/features.h":           {},
	"parallel/iterator.h":           {},
	"parallel/parallel.h":           {},
	"parallel/queue.h":              {},
	"parallel/settings.h":           {},
	"parallel/tags.h":               {},
	"parallel/types.h":              {},
	"tr1/complex.h":                 {},
	"tr1/math.h":                    {},
	"tr1/stdio.h":                   {},
	"tr1/type_traits.h":             {},
	"tr1/unordered_map.h":           {},
	"tr1/unordered_set.h":           {},
}

var cppBackwardQualifiedStdHeaderStemSet = map[string]struct{}{
	"hash_map": {},
	"hash_set": {},
}

var cppParallelQualifiedStdHeaderStemSet = map[string]struct{}{
	"algorithmfwd":         {},
	"balanced_quicksort":   {},
	"base":                 {},
	"basic_iterator":       {},
	"checkers":             {},
	"compiletime_settings": {},
	"equally_split":        {},
	"features":             {},
	"find":                 {},
	"find_selectors":       {},
	"for_each":             {},
	"for_each_selectors":   {},
	"iterator":             {},
	"list_partition":       {},
	"losertree":            {},
	"merge":                {},
	"multiseq_selection":   {},
	"multiway_merge":       {},
	"multiway_mergesort":   {},
	"numericfwd":           {},
	"omp_loop":             {},
	"omp_loop_static":      {},
	"par_loop":             {},
	"partial_sum":          {},
	"partition":            {},
	"quicksort":            {},
	"random_number":        {},
	"random_shuffle":       {},
	"search":               {},
	"set_operations":       {},
	"settings":             {},
	"sort":                 {},
	"tags":                 {},
	"types":                {},
	"unique_copy":          {},
	"workstealing":         {},
}

var cppExtPBDSQualifiedStdHeaderStemSet = map[string]struct{}{
	"assoc_container":    {},
	"hash_policy":        {},
	"list_update_policy": {},
	"priority_queue":     {},
	"tag_and_trait":      {},
	"tree_policy":        {},
	"trie_policy":        {},
}
