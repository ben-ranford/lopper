package jvm

import (
	"context"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"

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
	Files    []fileScan
	Warnings []string
}

func scanRepo(ctx context.Context, repoPath string, depPrefixes map[string]string, depAliases map[string]string) (scanResult, error) {
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
		return scanJVMSourceFile(repoPath, path, depPrefixes, depAliases, &result)
	})
	if err != nil {
		return result, err
	}

	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Java/Kotlin source files found for analysis")
	}
	return result, nil
}

func scanJVMSourceFile(repoPath string, path string, depPrefixes map[string]string, depAliases map[string]string, result *scanResult) error {
	if !isSourceFile(path) {
		return nil
	}
	var (
		content []byte
		err     error
	)
	if strings.TrimSpace(repoPath) == "" {
		content, err = safeio.ReadFile(path)
	} else {
		content, err = safeio.ReadFileUnder(repoPath, path)
	}
	if err != nil {
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

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt", ".kts":
		return true
	default:
		return false
	}
}

var (
	packagePattern = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][A-Za-z0-9_\.]*)\s*;?\s*$`)
	importPattern  = regexp.MustCompile(`(?m)^\s*import\s+(?:static\s+)?([A-Za-z_][A-Za-z0-9_\.]*)(\.\*)?(?:\s+as\s+([A-Za-z_][A-Za-z0-9_]*))?\s*;?\s*$`)
)

const importPatternMatchGroups = 4

func parsePackage(content []byte) string {
	matches := packagePattern.FindSubmatch(content)
	if len(matches) != 2 {
		return ""
	}
	return strings.TrimSpace(string(matches[1]))
}

func parseImports(content []byte, filePath string, filePackage string, depPrefixes map[string]string, depAliases map[string]string) []importBinding {
	return shared.ParseImportLines(content, filePath, func(line string, _ int) []shared.ImportRecord {
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

	return shared.ImportRecord{
		Dependency: dependency,
		Module:     module,
		Name:       symbol,
		Local:      localName,
		Wildcard:   wildcard,
	}, true
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
	return shared.CountUsage(content, imports)
}
