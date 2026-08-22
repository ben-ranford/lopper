package cpp

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
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

	seenUnresolvedEvidence map[unresolvedIncludeEvidence]struct{}
}

type includeResolver struct {
	repoPath                  string
	includeDirs               []string
	includeSearchPaths        []includeSearchPath
	sourceIncludeDirs         map[string][]includeSearchPath
	sourceIncludeSearchPaths  []includeSearchPath
	hasSourceIncludeSearchSet bool
	catalog                   dependencyCatalog
}

type scanInput struct {
	Path               string
	IncludeSearchPaths []includeSearchPath
	HasCompileCommand  bool
}

type includeLookup struct {
	sourcePath string
	header     string
	delimiter  byte
}

type includeSearchPath struct {
	Path            string
	System          bool
	QuoteOnly       bool
	ProvenanceKnown bool
}

type includeResolution struct {
	Path            string
	Resolved        bool
	System          bool
	ProvenanceKnown bool
}

type unresolvedIncludeEvidence struct {
	Header   string
	Location report.Location
}

type scanStage struct {
	scanner includeResolver
	result  scanResult
}

func scanRepo(ctx context.Context, repoPath string, compileInfo compileContext, catalog dependencyCatalog) (scanResult, error) {
	stage := scanStage{
		scanner: includeResolver{
			repoPath:           repoPath,
			includeDirs:        compileInfo.IncludeDirs,
			includeSearchPaths: compileInfo.IncludeSearchPaths,
			sourceIncludeDirs:  compileInfo.SourceIncludeDirs,
			catalog:            catalog,
		},
		result: scanResult{Catalog: catalog},
	}

	inputs, warnings, err := resolveScanInputs(ctx, repoPath, compileInfo)
	if err != nil {
		return stage.result, err
	}
	stage.result.Warnings = append(stage.result.Warnings, warnings...)
	if len(inputs) == 0 {
		stage.result.Warnings = append(stage.result.Warnings, "no C/C++ source files found for analysis")
		return stage.result, nil
	}

	for _, input := range inputs {
		if err := stage.process(ctx, input); err != nil {
			return stage.result, err
		}
	}
	stage.result.appendLargeFileWarning()
	stage.result.appendUnresolvedSummaryWarning()
	return stage.result, nil
}

func resolveScanInputs(ctx context.Context, repoPath string, compileInfo compileContext) ([]scanInput, []string, error) {
	if len(compileInfo.SourceContexts) > 0 {
		inputs, warnings, err := filterCompileSourceContexts(repoPath, compileInfo.SourceContexts)
		if err != nil {
			return nil, warnings, err
		}
		if len(inputs) > 0 {
			return inputs, warnings, nil
		}
		warnings = append(warnings, "compile database did not yield valid in-repo source files; falling back to repo scan")
		files, err := walkCPPFiles(ctx, repoPath)
		return scanInputsForFiles(files), warnings, err
	}
	if len(compileInfo.SourceFiles) > 0 {
		files, warnings, err := filterCompileSourceHints(repoPath, compileInfo.SourceFiles)
		if err != nil {
			return nil, warnings, err
		}
		if len(files) > 0 {
			return scanInputsForFiles(files), warnings, nil
		}
		warnings = append(warnings, "compile database did not yield valid in-repo source files; falling back to repo scan")
		files, err = walkCPPFiles(ctx, repoPath)
		return scanInputsForFiles(files), warnings, err
	}
	files, err := walkCPPFiles(ctx, repoPath)
	return scanInputsForFiles(files), nil, err
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

func filterCompileSourceContexts(repoPath string, contexts []compileSourceContext) ([]scanInput, []string, error) {
	inputs := make([]scanInput, 0, len(contexts))
	warnings := make([]string, 0)

	for _, context := range contexts {
		sourcePath := filepath.Clean(context.Path)
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

		inputs = append(inputs, scanInput{
			Path:               sourcePath,
			IncludeSearchPaths: append([]includeSearchPath(nil), context.IncludeSearchPaths...),
			HasCompileCommand:  true,
		})
	}

	return inputs, warnings, nil
}

func scanInputsForFiles(files []string) []scanInput {
	inputs := make([]scanInput, 0, len(files))
	for _, file := range files {
		inputs = append(inputs, scanInput{Path: file})
	}
	return inputs
}

func (s *scanStage) process(ctx context.Context, input scanInput) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}

	scanner := s.scanner
	if input.HasCompileCommand {
		scanner.sourceIncludeSearchPaths = input.IncludeSearchPaths
		scanner.hasSourceIncludeSearchSet = true
	}
	scanFile, unresolvedEvidence, err := scanner.scanFile(input.Path)
	if err != nil {
		if shared.IsPureSentinelError(err, safeio.ErrFileTooLarge) {
			s.result.SkippedLargeFiles++
			return nil
		}
		if strings.Contains(strings.ToLower(err.Error()), "path escapes root") {
			s.result.Warnings = append(s.result.Warnings, fmt.Sprintf("skipping compile database file outside repo boundary: %s", input.Path))
			return nil
		}
		return err
	}
	if len(scanFile.Includes) > 0 {
		s.result.Files = append(s.result.Files, scanFile)
	}
	s.result.recordUnresolvedEvidence(unresolvedEvidence)
	return nil
}

func (r *scanResult) recordUnresolvedEvidence(evidence []unresolvedIncludeEvidence) {
	for _, item := range evidence {
		if !r.recordUnresolvedItem(item) {
			continue
		}
		r.UnresolvedCount++
		r.appendSampleWarnings([]string{item.sample()})
	}
}

func (r *scanResult) recordUnresolvedItem(item unresolvedIncludeEvidence) bool {
	if r.seenUnresolvedEvidence == nil {
		r.seenUnresolvedEvidence = make(map[unresolvedIncludeEvidence]struct{})
	}
	if _, ok := r.seenUnresolvedEvidence[item]; ok {
		return false
	}
	r.seenUnresolvedEvidence[item] = struct{}{}
	return true
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

func (e *unresolvedIncludeEvidence) sample() string {
	return fmt.Sprintf("%s:%d:%s", e.Location.File, e.Location.Line, e.Header)
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

func (r *includeResolver) scanFile(path string) (fileScan, []unresolvedIncludeEvidence, error) {
	scan := fileScan{}
	content, err := safeio.ReadFileUnderLimit(r.repoPath, path, maxScannableCPPFile)
	if err != nil {
		return scan, nil, err
	}

	relative, err := filepath.Rel(r.repoPath, path)
	if err != nil {
		relative = path
	}
	scan.Path = relative

	parsed := parseIncludes(content)
	unresolvedEvidence := make([]unresolvedIncludeEvidence, 0)
	for _, include := range parsed {
		dependency, unresolved := r.mapIncludeToDependency(path, include)
		if dependency == "" {
			if unresolved {
				unresolvedEvidence = append(unresolvedEvidence, unresolvedIncludeEvidence{
					Header: include.Path,
					Location: report.Location{
						File:   relative,
						Line:   include.Line,
						Column: include.Column,
					},
				})
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

	return scan, unresolvedEvidence, nil
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
	if isAngleStdHeader(header, include.Delimiter) {
		return "", false
	}
	resolution := r.resolveIncludePath(includeLookup{
		sourcePath: sourcePath,
		header:     header,
		delimiter:  include.Delimiter,
	})
	if resolution.Resolved && shared.IsPathWithin(r.repoPath, resolution.Path) {
		return "", false
	}
	if include.Delimiter == '"' {
		return "", !resolution.Resolved
	}
	if dependency, handled := r.suppressedStdHeaderDependency(header, resolution); handled {
		return dependency, false
	}
	dependency := dependencyFromIncludePath(header)
	if dependency == "" {
		return "", true
	}
	return correlateDeclaredDependency(dependency, r.catalog), false
}

func isAngleStdHeader(header string, delimiter byte) bool {
	return delimiter == '<' && !strings.Contains(cleanIncludeHeader(header), "/") && isLikelyStdHeader(header)
}

func (r *includeResolver) suppressedStdHeaderDependency(header string, resolution includeResolution) (string, bool) {
	if !isLikelyStdHeader(header) || !shouldSuppressQualifiedStdHeader(header, resolution) {
		return "", false
	}
	if !resolution.Resolved && strings.Contains(cleanIncludeHeader(header), "/") {
		return declaredIncludeDependency(header, r.catalog), true
	}
	return "", true
}

func (r *includeResolver) resolveIncludePath(include includeLookup) includeResolution {
	sourceDir := filepath.Dir(include.sourcePath)
	header := filepath.FromSlash(cleanIncludeHeader(include.header))
	candidates := make([]includeResolution, 0)
	if include.delimiter == '"' {
		candidates = append(candidates, includeResolution{
			Path:            filepath.Join(sourceDir, header),
			ProvenanceKnown: true,
		})
	}
	for _, includePath := range r.includeSearchPathsForSource(include.sourcePath, include.delimiter) {
		candidates = append(candidates, includeResolution{
			Path:            filepath.Join(includePath.Path, header),
			System:          includePath.System,
			ProvenanceKnown: includePath.ProvenanceKnown,
		})
	}
	for _, candidate := range candidates {
		candidate.Path = filepath.Clean(candidate.Path)
		if _, err := os.Stat(candidate.Path); err != nil {
			continue
		}
		candidate.Resolved = true
		return candidate
	}
	return includeResolution{}
}

func (r *includeResolver) includeSearchPathsForSource(sourcePath string, delimiter byte) []includeSearchPath {
	if r.hasSourceIncludeSearchSet {
		return filterIncludeSearchPathsForDelimiter(r.sourceIncludeSearchPaths, delimiter)
	}
	if len(r.sourceIncludeDirs) > 0 {
		if paths, ok := r.sourceIncludeDirs[filepath.Clean(sourcePath)]; ok {
			return filterIncludeSearchPathsForDelimiter(paths, delimiter)
		}
	}
	if len(r.includeSearchPaths) > 0 {
		return filterIncludeSearchPathsForDelimiter(r.includeSearchPaths, delimiter)
	}
	if len(r.includeDirs) == 0 {
		return nil
	}
	paths := make([]includeSearchPath, 0, len(r.includeDirs))
	for _, includeDir := range r.includeDirs {
		paths = append(paths, includeSearchPath{Path: includeDir})
	}
	return paths
}

func filterIncludeSearchPathsForDelimiter(paths []includeSearchPath, delimiter byte) []includeSearchPath {
	if delimiter == '"' {
		return paths
	}
	if delimiter != '<' {
		return nil
	}
	filtered := make([]includeSearchPath, 0, len(paths))
	for _, includePath := range paths {
		if includePath.QuoteOnly {
			continue
		}
		filtered = append(filtered, includePath)
	}
	return filtered
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
	header = cleanIncludeHeader(header)
	if header == "" {
		return false
	}
	if strings.Contains(header, "/") {
		if isKnownOSCompilerQualifiedHeader(header) {
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

func cleanIncludeHeader(header string) string {
	header = strings.TrimSpace(filepath.ToSlash(header))
	if header == "" {
		return ""
	}
	cleaned := pathpkg.Clean(header)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func shouldSuppressQualifiedStdHeader(header string, resolution includeResolution) bool {
	if !strings.Contains(cleanIncludeHeader(header), "/") {
		return true
	}
	if !resolution.Resolved {
		return true
	}
	if resolution.ProvenanceKnown {
		return resolution.System && isLikelySystemIncludePath(resolution.Path)
	}
	return isLikelySystemIncludePath(resolution.Path)
}

func declaredIncludeDependency(header string, catalog dependencyCatalog) string {
	dependency := dependencyFromIncludePath(header)
	if dependency == "" || len(catalog.Declarations) == 0 {
		return ""
	}
	if catalog.contains(dependency) {
		return dependency
	}
	correlated := correlateDeclaredDependency(dependency, catalog)
	if correlated != dependency {
		return correlated
	}
	return ""
}

func isKnownOSCompilerQualifiedHeader(header string) bool {
	parts := strings.Split(header, "/")
	if len(parts) < 2 {
		return false
	}
	if isLikelyMultiarchIncludePrefix(parts[0]) && len(parts) >= 3 {
		parts = parts[1:]
	}
	namespace, leaf := parts[0], parts[len(parts)-1]
	if leaf == "" {
		return false
	}
	switch namespace {
	case "sys", "linux", "asm", "asm-generic":
		return filepath.Ext(leaf) == ".h"
	case "bits":
		return filepath.Ext(leaf) == ".h" || leaf == "stdc++.h"
	default:
		return false
	}
}

func isLikelyMultiarchIncludePrefix(prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || strings.ContainsAny(prefix, `/\`) {
		return false
	}
	parts := strings.Split(prefix, "-")
	if len(parts) < 3 {
		return false
	}
	if len(parts) == 3 && parts[1] == "w64" && parts[2] == "mingw32" {
		return isKnownMultiarchCPU(parts[0])
	}
	for i := 1; i < len(parts)-1; i++ {
		if parts[i] != "linux" {
			continue
		}
		arch := strings.Join(parts[:i], "-")
		abi := strings.Join(parts[i+1:], "-")
		return isKnownMultiarchCPU(arch) && isKnownLinuxMultiarchABI(abi)
	}
	return false
}

func isKnownMultiarchCPU(arch string) bool {
	switch arch {
	case "aarch64", "alpha", "arm", "arm64", "armel", "armhf", "hppa", "i386", "i486", "i586", "i686", "loongarch64", "m68k", "mips", "mips64", "mips64el", "mipsel", "powerpc", "powerpc64", "powerpc64le", "ppc64", "ppc64le", "riscv64", "s390x", "sparc64", "x86_64":
		return true
	default:
		return false
	}
}

func isKnownLinuxMultiarchABI(abi string) bool {
	switch abi {
	case "android", "gnu", "gnuabin32", "gnuabi64", "gnueabi", "gnueabihf", "gnux32", "musl", "musleabi", "musleabihf":
		return true
	default:
		return false
	}
}

func isLikelySystemIncludePath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	path = filepath.ToSlash(filepath.Clean(path))
	lowerPath := strings.ToLower(path)

	return hasSystemIncludeRoot(path) ||
		hasAppleSystemIncludePath(lowerPath) ||
		hasWindowsSystemIncludePath(path, lowerPath) ||
		hasCompilerRuntimeIncludePath(path)
}

func hasSystemIncludeRoot(path string) bool {
	for _, prefix := range []string{
		"/usr/include/",
		"/usr/local/include/",
		"/mingw/include/",
		"/mingw64/include/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func hasAppleSystemIncludePath(lowerPath string) bool {
	if strings.Contains(lowerPath, "/sdks/") && strings.Contains(lowerPath, ".sdk/usr/include/") {
		return true
	}
	return strings.Contains(lowerPath, ".xctoolchain/usr/lib/clang/") ||
		strings.Contains(lowerPath, "/library/developer/commandlinetools/usr/lib/clang/")
}

func hasWindowsSystemIncludePath(path, lowerPath string) bool {
	if !isWindowsStyleIncludePath(path) {
		return false
	}
	for _, fragment := range []string{
		"/mingw/include/",
		"/mingw64/include/",
		"/msvc/",
	} {
		if strings.Contains(lowerPath, fragment) {
			return true
		}
	}
	return false
}

func hasCompilerRuntimeIncludePath(path string) bool {
	for _, fragment := range []string{
		"/include/c++/",
		"/include/c++/v1/",
		"/lib/clang/",
		"/lib/gcc/",
		"/msvc/",
	} {
		if strings.Contains(path, fragment) {
			return true
		}
	}
	return false
}

func isWindowsStyleIncludePath(path string) bool {
	return len(path) >= 3 &&
		((path[1] == ':' && path[2] == '/') ||
			strings.HasPrefix(path, "//"))
}

func isKnownCompilerQualifiedStdHeader(header string) bool {
	header = strings.TrimSpace(filepath.ToSlash(header))
	parts := strings.Split(header, "/")
	switch len(parts) {
	case 2:
		return isKnownCompilerQualifiedStdHeaderLeaf(parts[0], parts[1])
	case 3:
		return isKnownNestedCompilerQualifiedStdHeader(parts[0], parts[1], parts[2])
	default:
		return false
	}
}

func isKnownCompilerQualifiedStdHeaderLeaf(namespace, leaf string) bool {
	if _, ok := cppQualifiedStdHeaderNamespaceSet[namespace]; !ok {
		return false
	}
	ext := filepath.Ext(leaf)
	stem := strings.TrimSuffix(leaf, ext)
	if stem == "" {
		return false
	}
	switch ext {
	case "":
		return isKnownCompilerQualifiedStdHeaderStem(namespace, stem)
	case ".h":
		return isKnownCompilerQualifiedStdHHeader(namespace, stem)
	default:
		return false
	}
}

func isKnownCompilerQualifiedStdHeaderStem(namespace, stem string) bool {
	if _, ok := cppStdHeaderSet[stem]; ok {
		return true
	}
	if namespace != "backward" {
		return false
	}
	_, ok := cppBackwardQualifiedStdHeaderStemSet[stem]
	return ok
}

func isKnownCompilerQualifiedStdHHeader(namespace, stem string) bool {
	var set map[string]struct{}
	switch namespace {
	case "backward":
		set = cppBackwardQualifiedStdHeaderHStemSet
	case "debug":
		set = cppDebugQualifiedStdHeaderHStemSet
	case "ext":
		set = cppExtQualifiedStdHeaderHStemSet
	case "parallel":
		set = cppParallelQualifiedStdHeaderHStemSet
	case "tr1":
		set = cppTR1QualifiedStdHeaderHStemSet
	default:
		return false
	}
	_, ok := set[stem]
	return ok
}

func isKnownNestedCompilerQualifiedStdHeader(namespace, subdir, leaf string) bool {
	if namespace != "ext" || subdir != "pb_ds" || filepath.Ext(leaf) != ".hpp" {
		return false
	}
	stem := strings.TrimSuffix(leaf, ".hpp")
	if stem == "" {
		return false
	}
	_, ok := cppExtPBDSQualifiedStdHeaderHPPStemSet[stem]
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

func makeStringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

var cppStdHeaderSet = map[string]struct{}{
	"algorithm": {}, "array": {}, "atomic": {}, "bitset": {}, "cassert": {}, "cctype": {}, "cerrno": {}, "cfenv": {}, "cfloat": {}, "charconv": {}, "chrono": {}, "cinttypes": {}, "ciso646": {}, "climits": {}, "clocale": {}, "cmath": {}, "codecvt": {}, "compare": {}, "complex": {}, "condition_variable": {}, "coroutine": {}, "csetjmp": {}, "csignal": {}, "cstdarg": {}, "cstddef": {}, "cstdint": {}, "cstdio": {}, "cstdlib": {}, "cstring": {}, "ctime": {}, "cuchar": {}, "cwchar": {}, "cwctype": {}, "deque": {}, "exception": {}, "execution": {}, "filesystem": {}, "forward_list": {}, "fstream": {}, "functional": {}, "future": {}, "initializer_list": {}, "iomanip": {}, "ios": {}, "iosfwd": {}, "iostream": {}, "istream": {}, "iterator": {}, "latch": {}, "limits": {}, "list": {}, "locale": {}, "map": {}, "memory": {}, "memory_resource": {}, "mutex": {}, "new": {}, "numbers": {}, "numeric": {}, "optional": {}, "ostream": {}, "queue": {}, "random": {}, "ranges": {}, "ratio": {}, "regex": {}, "scoped_allocator": {}, "semaphore": {}, "set": {}, "shared_mutex": {}, "source_location": {}, "span": {}, "sstream": {}, "stack": {}, "stdexcept": {}, "stop_token": {}, "streambuf": {}, "string": {}, "string_view": {}, "strstream": {}, "syncstream": {}, "system_error": {}, "thread": {}, "tuple": {}, "type_traits": {}, "typeindex": {}, "typeinfo": {}, "unordered_map": {}, "unordered_set": {}, "utility": {}, "valarray": {}, "variant": {}, "vector": {},
	"assert": {}, "ctype": {}, "errno": {}, "float": {}, "inttypes": {}, "math": {}, "setjmp": {}, "signal": {}, "stdarg": {}, "stddef": {}, "stdint": {}, "stdio": {}, "stdlib": {}, "time": {}, "wchar": {}, "wctype": {},
}

var cppQualifiedStdHeaderNamespaceSet = makeStringSet(
	"backward",
	"debug",
	"experimental",
	"ext",
	"parallel",
	"tr1",
	"tr2",
)

var cppBackwardQualifiedStdHeaderStemSet = makeStringSet(
	"hash_map",
	"hash_set",
)

var cppBackwardQualifiedStdHeaderHStemSet = makeStringSet(
	"auto_ptr",
	"backward_warning",
	"binders",
	"hash_fun",
	"hashtable",
)

var cppDebugQualifiedStdHeaderHStemSet = makeStringSet(
	"assertions",
	"debug",
	"functions",
	"macros",
	"map",
	"safe_base",
	"safe_iterator",
	"set",
)

var cppExtQualifiedStdHeaderHStemSet = makeStringSet(
	"aligned_buffer",
	"alloc_traits",
	"atomicity",
	"numeric_traits",
	"type_traits",
)

var cppParallelQualifiedStdHeaderHStemSet = makeStringSet(
	"algo",
	"algobase",
	"algorithmfwd",
	"balanced_quicksort",
	"base",
	"basic_iterator",
	"checkers",
	"compatibility",
	"compiletime_settings",
	"equally_split",
	"features",
	"find",
	"find_selectors",
	"for_each",
	"for_each_selectors",
	"iterator",
	"list_partition",
	"losertree",
	"merge",
	"multiseq_selection",
	"multiway_merge",
	"multiway_mergesort",
	"numericfwd",
	"omp_loop",
	"omp_loop_static",
	"par_loop",
	"partial_sum",
	"partition",
	"parallel",
	"quicksort",
	"queue",
	"random_number",
	"random_shuffle",
	"search",
	"set_operations",
	"settings",
	"sort",
	"tags",
	"types",
	"unique_copy",
	"workstealing",
)

var cppTR1QualifiedStdHeaderHStemSet = makeStringSet(
	"complex",
	"ctype",
	"float",
	"inttypes",
	"limits",
	"math",
	"random",
	"stdarg",
	"stdio",
	"stdint",
	"stdlib",
	"type_traits",
	"unordered_map",
	"unordered_set",
	"wchar",
	"wctype",
)

var cppExtPBDSQualifiedStdHeaderHPPStemSet = makeStringSet(
	"assoc_container",
	"exception",
	"hash_policy",
	"list_update_policy",
	"priority_queue",
	"tag_and_trait",
	"tree_policy",
	"trie_policy",
)
