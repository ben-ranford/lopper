package jvm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Package string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files             []fileScan
	Warnings          []string
	SkippedLargeFiles int
	SkippedSymlinks   int
}

func scanRepo(ctx context.Context, repoPath string, depPrefixes map[string]string, depAliases map[string]string) (scanResult, error) {
	return scanRepoWithSourceReader(ctx, repoPath, depPrefixes, depAliases, safeio.ReadFileUnderLimit)
}

func scanRepoWithinRoot(ctx context.Context, repoPath string, root safeio.Root, depPrefixes map[string]string, depAliases map[string]string) (scanResult, error) {
	result := scanResult{}
	if repoPath == "" {
		return result, fs.ErrInvalid
	}

	budget := shared.RootedWalkBudget{
		MaxTraversalEntries: maxJVMSourceTraversalEntries,
		MaxFiles:            maxJVMSourceFiles,
		MaxWorkItems:        maxJVMSourceWorkItems,
		CountCandidate: func(path string, _ fs.DirEntry) bool {
			return isSourceFile(path)
		},
	}
	err := shared.WalkRepoFilesWithinRootPinned(ctx, repoPath, root, budget, shouldSkipDir, func(file shared.RootedWalkFile) error {
		readSource := rootedJVMSourceReader(file.Parent, file.Leaf)
		return scanJVMSourceFileWithReader(repoPath, file.Path, file.Entry, depPrefixes, depAliases, &result, readSource)
	})
	if err != nil {
		if warning, limited := shared.RootedWalkBudgetWarning("JVM source scan", budget, err); limited {
			result.Warnings = append(result.Warnings, warning)
		} else {
			return result, err
		}
	}

	if result.SkippedLargeFiles > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d large JVM file(s) above %d bytes", result.SkippedLargeFiles, maxScannableJVMSourceFile))
	}
	if result.SkippedSymlinks > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d unreadable or untrusted JVM source symlink(s)", result.SkippedSymlinks))
	}
	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Java/Kotlin source files found for analysis")
	}
	return result, nil
}

type jvmSourceReader func(rootDir, targetPath string, maxBytes int64) ([]byte, error)

func rootedJVMSourceReader(parent safeio.Root, leaf string) jvmSourceReader {
	return func(_, _ string, maxBytes int64) ([]byte, error) {
		return safeio.ReadFileWithinRootLimit(parent, leaf, maxBytes)
	}
}

func scanRepoWithSourceReader(ctx context.Context, repoPath string, depPrefixes map[string]string, depAliases map[string]string, readSource jvmSourceReader) (scanResult, error) {
	result := scanResult{}
	if repoPath == "" {
		return result, fs.ErrInvalid
	}

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		return scanJVMSourceFileWithReader(repoPath, path, entry, depPrefixes, depAliases, &result, readSource)
	})
	if err != nil {
		return result, err
	}

	if result.SkippedLargeFiles > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d large JVM file(s) above %d bytes", result.SkippedLargeFiles, maxScannableJVMSourceFile))
	}
	if result.SkippedSymlinks > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("skipped %d unreadable or untrusted JVM source symlink(s)", result.SkippedSymlinks))
	}
	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Java/Kotlin source files found for analysis")
	}
	return result, nil
}

func scanJVMSourceFile(repoPath string, path string, depPrefixes map[string]string, depAliases map[string]string, result *scanResult) error {
	return scanJVMSourceFileWithReader(repoPath, path, nil, depPrefixes, depAliases, result, safeio.ReadFileUnderLimit)
}

func scanJVMSourceFileWithReader(repoPath string, path string, entry fs.DirEntry, depPrefixes map[string]string, depAliases map[string]string, result *scanResult, readSource jvmSourceReader) error {
	if !isSourceFile(path) {
		return nil
	}
	var (
		content []byte
		err     error
	)
	if strings.TrimSpace(repoPath) == "" {
		content, err = safeio.ReadFileLimit(path, maxScannableJVMSourceFile)
	} else {
		content, err = readSource(repoPath, path, maxScannableJVMSourceFile)
	}
	if shared.IsPureSentinelError(err, safeio.ErrFileTooLarge) {
		if result != nil {
			result.SkippedLargeFiles++
		}
		return nil
	}
	if err != nil {
		if warning, skip := classifySkippableJVMSourceReadError(repoPath, path, entry, err); skip {
			if result != nil {
				result.SkippedSymlinks++
				result.Warnings = append(result.Warnings, warning)
			}
			return nil
		}
		return err
	}
	relativePath, err := filepath.Rel(repoPath, path)
	if err != nil {
		relativePath = path
	}

	filePackage := parsePackage(content)
	imports := parseImports(content, relativePath, filePackage, depPrefixes, depAliases)
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Package: filePackage,
		Imports: imports,
		Usage:   countUsage(content, imports),
	})
	return nil
}

func classifySkippableJVMSourceReadError(repoPath, path string, entry fs.DirEntry, err error) (string, bool) {
	if err == nil || errors.Is(err, context.Canceled) {
		return "", false
	}

	description, expected := describeExpectedJVMSourceReadError(err)
	if !expected {
		return "", false
	}
	if entry == nil || entry.Type()&os.ModeSymlink == 0 {
		return "", false
	}

	return fmt.Sprintf("skipped JVM source symlink %s: %s", relativeSourceScanPath(repoPath, path), description), true
}

func relativeSourceScanPath(repoPath, path string) string {
	if strings.TrimSpace(repoPath) == "" {
		return path
	}
	relativePath, relErr := filepath.Rel(repoPath, path)
	if relErr != nil {
		return path
	}
	return relativePath
}

func describeExpectedJVMSourceReadError(err error) (string, bool) {
	switch {
	case shared.IsPureSentinelError(err, fs.ErrNotExist):
		return "target missing", true
	case shared.IsPureSentinelError(err, fs.ErrPermission):
		return "target unreadable", true
	case shared.IsPureSentinelError(err, safeio.ErrTargetPathSymlink):
		return "target is an untrusted symlink", true
	case isJVMSourceSymlinkLoopError(err):
		return "target loops through symlinks", true
	case isJVMSourceRepoRootEscapeError(err):
		return "target escapes repo root", true
	default:
		return "", false
	}
}

func isJVMSourceRepoRootEscapeError(err error) bool {
	return shared.IsPureSentinelError(err, safeio.ErrPathEscapesRoot)
}

func isJVMSourceSymlinkLoopError(err error) bool {
	return shared.IsPureSentinelError(err, syscall.ELOOP)
}

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt", ".kts":
		return true
	default:
		return false
	}
}

const (
	importPatternMatchGroups   = 4
	jvmImportIdentifierPattern = "(?:[A-Za-z_][A-Za-z0-9_]*|`[A-Za-z_][A-Za-z0-9_]*`)"
)

var (
	packagePattern = regexp.MustCompile(`(?m)^\s*package\s+((?:[A-Za-z_][A-Za-z0-9_]*|` + "`[^`\\r\\n]+`" + `)(?:\.(?:[A-Za-z_][A-Za-z0-9_]*|` + "`[^`\\r\\n]+`" + `))*)\s*;?\s*$`)
	importPattern  = regexp.MustCompile(`^\s*import\s+(?:static\s+)?(` + jvmImportIdentifierPattern + `(?:\.` + jvmImportIdentifierPattern + `)*)(\.\*)?(?:\s+as\s+(` + jvmImportIdentifierPattern + `))?\s*;?\s*$`)
)

var kotlinHardKeywords = map[string]struct{}{
	"as": {}, "break": {}, "class": {}, "continue": {}, "do": {}, "else": {}, "false": {},
	"for": {}, "fun": {}, "if": {}, "in": {}, "interface": {}, "is": {}, "null": {}, "object": {},
	"package": {}, "return": {}, "super": {}, "this": {}, "throw": {}, "true": {}, "try": {},
	"typealias": {}, "typeof": {}, "val": {}, "var": {}, "when": {}, "while": {},
}

func parsePackage(content []byte) string {
	matches := packagePattern.FindSubmatch(content)
	if len(matches) != 2 {
		return ""
	}
	return strings.TrimSpace(string(matches[1]))
}

func parseImports(content []byte, filePath string, filePackage string, depPrefixes map[string]string, depAliases map[string]string) []importBinding {
	sanitized := shared.StripBlockComments(content)
	return shared.ParseImportLines(sanitized, filePath, func(line string, _ int) []shared.ImportRecord {
		line = stripLineComment(line)
		matches := importPattern.FindStringSubmatch(line)
		if len(matches) != importPatternMatchGroups {
			return nil
		}
		module := strings.TrimSpace(matches[1])
		if module == "" || shouldIgnoreImport(module, filePackage) {
			return nil
		}

		dependency := resolveDependency(module, depPrefixes, depAliases)
		if dependency == "" {
			dependency = fallbackDependency(module)
		}
		if dependency == "" {
			return nil
		}

		record, ok := buildImportRecord(matches, module, dependency)
		if !ok {
			return nil
		}

		return []shared.ImportRecord{record}
	})
}

func buildImportRecord(matches []string, module string, dependency string) (shared.ImportRecord, bool) {
	wildcard := strings.TrimSpace(matches[2]) == ".*"
	symbol := lastModuleSegment(module)
	if wildcard {
		symbol = "*"
	}
	if symbol == "" {
		return shared.ImportRecord{}, false
	}

	localName := symbol
	if alias := strings.TrimSpace(matches[3]); alias != "" && !wildcard {
		localName = alias
	}
	localName = strings.TrimPrefix(strings.TrimSuffix(localName, "`"), "`")

	return shared.ImportRecord{
		Dependency: dependency,
		Module:     module,
		Name:       symbol,
		Local:      localName,
		Wildcard:   wildcard,
	}, true
}

func isKotlinEscapedKeyword(local string) bool {
	if len(local) < 3 || local[0] != '`' || local[len(local)-1] != '`' {
		return false
	}
	_, ok := kotlinHardKeywords[local[1:len(local)-1]]
	return ok
}

func stripLineComment(line string) string {
	return shared.StripLineComment(line, "//")
}

func shouldIgnoreImport(module, filePackage string) bool {
	module = strings.TrimSpace(module)
	if module == "" {
		return true
	}

	stdlibPrefixes := []string{
		"java.", "javax.", "kotlin.", "jdk.", "sun.",
	}
	for _, prefix := range stdlibPrefixes {
		if strings.HasPrefix(module, prefix) {
			return true
		}
	}

	if filePackage != "" {
		if module == filePackage || strings.HasPrefix(module, filePackage+".") {
			return true
		}
	}
	return false
}

func resolveDependency(module string, depPrefixes map[string]string, depAliases map[string]string) string {
	best := ""
	bestLen := 0

	for prefix, dependency := range depPrefixes {
		if module == prefix || strings.HasPrefix(module, prefix+".") {
			if len(prefix) > bestLen {
				best = dependency
				bestLen = len(prefix)
			}
		}
	}
	if best != "" {
		return best
	}

	parts := strings.Split(module, ".")
	for i := len(parts); i >= 1; i-- {
		key := strings.Join(parts[:i], ".")
		if dependency, ok := depAliases[key]; ok {
			return dependency
		}
	}

	return ""
}

func fallbackDependency(module string) string {
	parts := strings.Split(module, ".")
	if len(parts) >= 2 {
		return normalizeDependencyID(parts[0] + "." + parts[1])
	}
	if len(parts) == 1 {
		return normalizeDependencyID(parts[0])
	}
	return ""
}

func lastModuleSegment(module string) string {
	parts := strings.Split(module, ".")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

func firstContentColumn(line string) int {
	return shared.FirstContentColumn(line)
}

func countUsage(content []byte, imports []importBinding) map[string]int {
	scannable := maskKotlinDirectiveLines(shared.MaskCommentsAndStringsForFile(content, "source.kt"))
	escapedLocals := escapedKotlinImportLocals(content, imports)
	if len(escapedLocals) == 0 {
		return shared.CountUsage(scannable, imports)
	}

	bareImports := make([]shared.ImportRecord, 0, len(imports))
	for _, imported := range imports {
		if _, escaped := escapedLocals[imported.Local]; !escaped {
			bareImports = append(bareImports, imported)
		}
	}
	usage := shared.CountUsage(scannable, bareImports)
	for index := 0; index < len(scannable); index++ {
		local, end, ok := escapedIdentifierAt(scannable, index)
		if !ok {
			continue
		}
		if _, escaped := escapedLocals[local]; escaped {
			usage[local]++
		}
		index = end
	}
	return usage
}

func escapedKotlinImportLocals(content []byte, imports []importBinding) map[string]struct{} {
	imported := make(map[string]struct{}, len(imports))
	for _, record := range imports {
		imported[record.Local] = struct{}{}
	}
	escaped := make(map[string]struct{})
	bare := make(map[string]struct{})
	for _, line := range strings.Split(string(shared.StripBlockComments(content)), "\n") {
		matches := importPattern.FindStringSubmatch(stripLineComment(line))
		if len(matches) != importPatternMatchGroups || strings.TrimSpace(matches[2]) == ".*" {
			continue
		}
		local := strings.TrimSpace(matches[3])
		if local == "" {
			local = lastModuleSegment(strings.TrimSpace(matches[1]))
		}
		if isKotlinEscapedKeyword(local) {
			local = strings.Trim(local, "`")
			if _, ok := imported[local]; ok {
				escaped[local] = struct{}{}
			}
			continue
		}
		local = strings.Trim(local, "`")
		if _, ok := imported[local]; ok {
			bare[local] = struct{}{}
		}
	}
	for local := range bare {
		delete(escaped, local)
	}
	return escaped
}

func maskKotlinDirectiveLines(content []byte) []byte {
	masked := append([]byte(nil), content...)
	for start := 0; start < len(masked); {
		end := start
		for end < len(masked) && masked[end] != '\n' {
			end++
		}
		fields := strings.Fields(string(masked[start:end]))
		if len(fields) > 0 && (fields[0] == "import" || fields[0] == "package") {
			for index := start; index < end; index++ {
				masked[index] = ' '
			}
		}
		start = end + 1
	}
	return masked
}

func escapedIdentifierAt(content []byte, start int) (string, int, bool) {
	if content[start] != '`' {
		return "", start, false
	}
	end := start + 1
	for end < len(content) && (content[end] == '_' || content[end] >= 'a' && content[end] <= 'z' || content[end] >= 'A' && content[end] <= 'Z' || content[end] >= '0' && content[end] <= '9') {
		end++
	}
	if end == start+1 || end >= len(content) || content[end] != '`' {
		return "", start, false
	}
	return string(content[start+1 : end]), end, true
}
