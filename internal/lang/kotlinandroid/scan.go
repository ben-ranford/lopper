package kotlinandroid

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
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
	Files                  []fileScan
	Warnings               []string
	AmbiguousDependencies  map[string]struct{}
	UndeclaredDependencies map[string]struct{}

	fallbackModules  map[string]string
	ambiguousModules map[string][]string
}

func newScanResult() scanResult {
	return scanResult{
		AmbiguousDependencies:  make(map[string]struct{}),
		UndeclaredDependencies: make(map[string]struct{}),
		fallbackModules:        make(map[string]string),
		ambiguousModules:       make(map[string][]string),
	}
}

func (s *scanResult) addFallbackModule(module string, dependency string, declared bool) {
	module = strings.TrimSpace(module)
	if module == "" {
		return
	}
	if _, ok := s.fallbackModules[module]; !ok {
		s.fallbackModules[module] = dependency
	}
	if !declared {
		s.UndeclaredDependencies[normalizeDependencyID(dependency)] = struct{}{}
	}
}

func (s *scanResult) addAmbiguousModule(module string, candidates []string, chosen string) {
	module = strings.TrimSpace(module)
	if module == "" {
		return
	}
	if _, ok := s.ambiguousModules[module]; !ok {
		s.ambiguousModules[module] = append([]string{}, candidates...)
	}
	s.AmbiguousDependencies[normalizeDependencyID(chosen)] = struct{}{}
}

func (s *scanResult) appendInferenceWarnings() {
	if len(s.fallbackModules) > 0 {
		examples := make([]string, 0, len(s.fallbackModules))
		for module, dependency := range s.fallbackModules {
			examples = append(examples, module+" -> "+dependency)
		}
		sort.Strings(examples)
		if len(examples) > 3 {
			examples = examples[:3]
		}
		warning := fmt.Sprintf("%d import(s) were conservatively attributed because no declared Gradle mapping matched (examples: %s)", len(s.fallbackModules), strings.Join(examples, "; "))
		s.Warnings = append(s.Warnings, warning)
	}

	if len(s.UndeclaredDependencies) > 0 {
		undeclared := make([]string, 0, len(s.UndeclaredDependencies))
		for dependency := range s.UndeclaredDependencies {
			undeclared = append(undeclared, dependency)
		}
		sort.Strings(undeclared)
		s.Warnings = append(s.Warnings, "imports were attributed to undeclared dependencies: "+strings.Join(undeclared, ", "))
	}

	if len(s.ambiguousModules) > 0 {
		examples := make([]string, 0, len(s.ambiguousModules))
		for module, candidates := range s.ambiguousModules {
			examples = append(examples, module+" -> "+strings.Join(candidates, "|"))
		}
		sort.Strings(examples)
		if len(examples) > 3 {
			examples = examples[:3]
		}
		warning := fmt.Sprintf("%d import(s) matched multiple Gradle dependencies; deterministic fallback selected first candidate (examples: %s)", len(s.ambiguousModules), strings.Join(examples, "; "))
		s.Warnings = append(s.Warnings, warning)
	}
}

func scanRepo(ctx context.Context, repoPath string, lookups dependencyLookups) (scanResult, error) {
	result := newScanResult()
	if repoPath == "" {
		return result, fs.ErrInvalid
	}

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		return scanKotlinAndroidSourceFile(repoPath, path, lookups, &result)
	})
	if err != nil {
		return result, err
	}

	if len(result.Files) == 0 {
		result.Warnings = append(result.Warnings, "no Kotlin/Java source files found for analysis")
	}
	result.appendInferenceWarnings()
	return result, nil
}

func scanKotlinAndroidSourceFile(repoPath string, path string, lookups dependencyLookups, result *scanResult) error {
	if !isSourceFile(path) {
		return nil
	}
	content, relativePath, err := readKotlinAndroidSource(repoPath, path)
	if err != nil {
		return err
	}
	filePackage := parsePackage(content)
	imports := parseImports(content, relativePath, filePackage, lookups, result)
	result.Files = append(result.Files, fileScan{
		Path:    relativePath,
		Package: filePackage,
		Imports: imports,
		Usage:   countUsage(content, imports),
	})
	return nil
}

func readKotlinAndroidSource(repoPath, path string) ([]byte, string, error) {
	if strings.TrimSpace(repoPath) == "" {
		content, err := safeio.ReadFile(path)
		if err != nil {
			return nil, "", err
		}
		return content, path, nil
	}
	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return nil, "", err
	}
	relativePath := path
	if rel, relErr := filepath.Rel(repoPath, path); relErr == nil {
		relativePath = rel
	}
	return content, relativePath, nil
}

func isSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".java", ".kt":
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

func parseImports(content []byte, filePath string, filePackage string, lookups dependencyLookups, result *scanResult) []importBinding {
	return shared.ParseImportLines(content, filePath, func(line string, _ int) []shared.ImportRecord {
		line = stripLineComment(line)
		matches := importPattern.FindStringSubmatch(line)
		if len(matches) != importPatternMatchGroups {
			return nil
		}
		module := strings.TrimSpace(matches[1])
		if module == "" {
			return nil
		}

		dependency, ambiguous := resolveDependency(module, lookups)
		if shouldIgnoreImport(module, filePackage) && dependency == "" {
			return nil
		}
		if dependency == "" {
			dependency = fallbackDependency(module)
			if dependency == "" {
				return nil
			}
			_, declared := lookups.DeclaredDependencies[normalizeDependencyID(dependency)]
			result.addFallbackModule(module, dependency, declared)
		} else if len(ambiguous) > 1 {
			result.addAmbiguousModule(module, ambiguous, dependency)
		}

		record, ok := buildImportRecord(matches, module, dependency)
		if !ok {
			return nil
		}
		return []shared.ImportRecord{record}
	})
}

func buildImportRecord(matches []string, module string, dependency string) (shared.ImportRecord, bool) {
	symbol, wildcard := resolvedImportSymbol(matches, module)
	if symbol == "" {
		return shared.ImportRecord{}, false
	}
	localName := symbol
	alias := ""
	if len(matches) > 3 {
		alias = strings.TrimSpace(matches[3])
	}
	if alias != "" && !wildcard {
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

func resolvedImportSymbol(matches []string, module string) (string, bool) {
	if len(matches) > 2 && strings.TrimSpace(matches[2]) == ".*" {
		return "*", true
	}
	return lastModuleSegment(module), false
}

func stripLineComment(line string) string {
	return shared.StripLineComment(line, "//")
}

func shouldIgnoreImport(module, filePackage string) bool {
	module = strings.TrimSpace(module)
	if module == "" {
		return true
	}

	frameworkPrefixes := []string{
		"java.", "javax.", "kotlin.", "jdk.", "sun.", "android.",
	}
	for _, prefix := range frameworkPrefixes {
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

func resolveDependency(module string, lookups dependencyLookups) (string, []string) {
	best := ""
	bestLen := 0
	bestAmbiguous := []string(nil)

	for prefix, dependency := range lookups.Prefixes {
		if module != prefix && !strings.HasPrefix(module, prefix+".") {
			continue
		}
		if len(prefix) <= bestLen {
			continue
		}
		best = dependency
		bestLen = len(prefix)
		if ambiguous, ok := lookups.Ambiguous[prefix]; ok {
			bestAmbiguous = append([]string{}, ambiguous...)
		} else {
			bestAmbiguous = nil
		}
	}
	if best != "" {
		return best, bestAmbiguous
	}

	parts := strings.Split(module, ".")
	for i := len(parts); i >= 1; i-- {
		key := strings.Join(parts[:i], ".")
		dependency, ok := lookups.Aliases[key]
		if !ok {
			continue
		}
		if ambiguous, ambiguousOK := lookups.Ambiguous[key]; ambiguousOK {
			return dependency, append([]string{}, ambiguous...)
		}
		return dependency, nil
	}

	return "", nil
}

func fallbackDependency(module string) string {
	return shared.FallbackDependency(module, normalizeDependencyID)
}

func lastModuleSegment(module string) string {
	return shared.LastModuleSegment(module)
}

func countUsage(content []byte, imports []importBinding) map[string]int {
	return shared.CountUsage(content, imports)
}
