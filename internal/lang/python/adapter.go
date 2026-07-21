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

	return shared.ParseImportLines(content, filePath, func(line string, index int) []shared.ImportRecord {
		lineNoComment := stripComment(line)
		trimmed := strings.TrimSpace(lineNoComment)

		if records, handled := continuePendingFromImport(&pending, trimmed, lineNoComment, filePath, repoPath, index); handled {
			return records
		}

		return parseImportRecord(trimmed, lineNoComment, filePath, repoPath, index, &pending)
	})
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
		if _, err := os.Stat(filepath.Join(searchRoot, root+".py")); err == nil {
			return true
		}
		if _, err := os.Stat(filepath.Join(searchRoot, root, "__init__.py")); err == nil {
			return true
		}
	}
	return false
}

func localModuleSearchRoots(repoPath string) []string {
	roots := []string{repoPath}

	srcRoot := filepath.Join(repoPath, "src")
	if info, err := os.Stat(srcRoot); err == nil && info.IsDir() {
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
