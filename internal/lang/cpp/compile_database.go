package cpp

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	includeFlag         = "-I"
	isystemFlag         = "-isystem"
	iquoteFlag          = "-iquote"
	maxCompileDatabases = 64
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
	IncludeSearchPaths []includeSearchPath
	SourceIncludeDirs  map[string][]includeSearchPath
	SourceFiles        []string
	Warnings           []string
}

type compileContextCollector struct {
	repoPath           string
	includeDirSet      map[string]struct{}
	includeSearchPaths map[string]includeSearchPath
	sourceIncludeDirs  map[string][]includeSearchPath
	sourceFileSet      map[string]struct{}
	warnings           []string
	visited            int
	found              bool
}

func loadCompileContext(repoPath string) (compileContext, error) {
	collector, err := newCompileContextCollector(repoPath)
	if err != nil {
		return compileContext{}, err
	}

	err = shared.WalkRepoFiles(context.Background(), repoPath, 0, shared.ShouldSkipCommonDir, func(path string, entry fs.DirEntry) error {
		return collector.visit(path)
	})
	if err != nil {
		return compileContext{}, err
	}
	return collector.result(), nil
}

func newCompileContextCollector(repoPath string) (*compileContextCollector, error) {
	if repoPath == "" {
		return nil, fmt.Errorf("repo path is empty")
	}
	return &compileContextCollector{
		repoPath:           repoPath,
		includeDirSet:      make(map[string]struct{}),
		includeSearchPaths: make(map[string]includeSearchPath),
		sourceIncludeDirs:  make(map[string][]includeSearchPath),
		sourceFileSet:      make(map[string]struct{}),
	}, nil
}

func (c *compileContextCollector) visit(path string) error {
	if filepath.Base(path) != compileCommandsFile {
		return nil
	}
	c.visited++
	if c.visited > maxCompileDatabases {
		return fs.SkipAll
	}

	warnings, err := collectCompileDatabase(path, c.repoPath, c.includeDirSet, c.includeSearchPaths, c.sourceIncludeDirs, c.sourceFileSet)
	c.warnings = append(c.warnings, warnings...)
	if err != nil {
		return err
	}
	c.found = true
	return nil
}

func (c *compileContextCollector) result() compileContext {
	result := compileContext{
		HasCompileDatabase: c.found,
		IncludeDirs:        shared.SortedKeys(c.includeDirSet),
		IncludeSearchPaths: sortedIncludeSearchPaths(c.includeSearchPaths),
		SourceIncludeDirs:  copySourceIncludeDirs(c.sourceIncludeDirs),
		SourceFiles:        shared.SortedKeys(c.sourceFileSet),
		Warnings:           append([]string(nil), c.warnings...),
	}
	if !result.HasCompileDatabase {
		result.Warnings = append(result.Warnings, "compile_commands.json not found; using include-graph heuristics without translation unit context")
	}
	return result
}

func collectCompileDatabase(path, repoPath string, includeDirSet map[string]struct{}, includeSearchPathSet map[string]includeSearchPath, sourceIncludeDirs map[string][]includeSearchPath, sourceFileSet map[string]struct{}) ([]string, error) {
	entries, warnings, err := readCompileDatabase(path, repoPath)
	if err != nil || len(entries) == 0 {
		return warnings, err
	}

	for _, entry := range entries {
		baseDir := resolveCompileDirectory(path, entry.Directory)
		sourcePath := resolveCompilePath(baseDir, entry.File)
		recordCompileSource(sourceFileSet, sourcePath)
		includePaths := extractIncludeSearchPaths(entry.compileArgs(), baseDir)
		recordCompileIncludes(includeDirSet, includeSearchPathSet, includePaths)
		if sourcePath != "" && isCPPSourceFile(sourcePath) {
			sourceIncludeDirs[sourcePath] = mergeIncludeSearchPaths(sourceIncludeDirs[sourcePath], includePaths)
		}
	}
	return warnings, nil
}

func readCompileDatabase(path, repoPath string) ([]compileCommandEntry, []string, error) {
	relPath := relOrBase(repoPath, path)
	content, err := safeio.ReadFileUnderLimit(repoPath, path, maxCompileDatabaseBytes)
	switch {
	case err == nil:
	case shared.IsPureSentinelError(err, safeio.ErrFileTooLarge):
		return nil, []string{oversizedCPPInputWarning(relPath, maxCompileDatabaseBytes)}, nil
	default:
		return nil, nil, err
	}

	var entries []compileCommandEntry
	if err := json.Unmarshal(content, &entries); err != nil {
		return nil, []string{fmt.Sprintf("failed to parse %s: %v", relPath, err)}, nil
	}
	return entries, nil, nil
}

func (e *compileCommandEntry) compileArgs() []string {
	if len(e.Arguments) > 0 {
		return e.Arguments
	}
	if e.Command == "" {
		return nil
	}
	return strings.Fields(e.Command)
}

func recordCompileSource(sourceFileSet map[string]struct{}, sourcePath string) {
	if sourcePath == "" || !isCPPSourceFile(sourcePath) {
		return
	}
	sourceFileSet[sourcePath] = struct{}{}
}

func recordCompileIncludes(includeDirSet map[string]struct{}, includeSearchPathSet map[string]includeSearchPath, includePaths []includeSearchPath) {
	for _, includePath := range includePaths {
		if includePath.Path == "" {
			continue
		}
		includeDirSet[includePath.Path] = struct{}{}
		existing, ok := includeSearchPathSet[includePath.Path]
		switch {
		case !ok:
			includeSearchPathSet[includePath.Path] = includePath
		case includePath.System && !existing.System:
			includeSearchPathSet[includePath.Path] = includePath
		case existing.QuoteOnly && !includePath.QuoteOnly:
			includeSearchPathSet[includePath.Path] = includePath
		}
	}
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
	paths := extractIncludeSearchPaths(args, baseDir)
	seen := make(map[string]struct{})
	items := make([]string, 0)
	for _, includePath := range paths {
		addIncludeDir(includePath.Path, seen, &items)
	}
	sort.Strings(items)
	return items
}

func extractIncludeSearchPaths(args []string, baseDir string) []includeSearchPath {
	items := make([]includeSearchPath, 0)
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
			addIncludeSearchPath(resolveCompilePath(baseDir, args[i]), arg == isystemFlag, arg == iquoteFlag, &items)
		case strings.HasPrefix(arg, includeFlag) && len(arg) > len(includeFlag):
			addIncludeSearchPath(resolveCompilePath(baseDir, arg[len(includeFlag):]), false, false, &items)
		case strings.HasPrefix(arg, isystemFlag) && len(arg) > len(isystemFlag):
			addIncludeSearchPath(resolveCompilePath(baseDir, arg[len(isystemFlag):]), true, false, &items)
		case strings.HasPrefix(arg, iquoteFlag) && len(arg) > len(iquoteFlag):
			addIncludeSearchPath(resolveCompilePath(baseDir, arg[len(iquoteFlag):]), false, true, &items)
		}
	}
	return normalizeCompileSearchPaths(items)
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

func addIncludeSearchPath(path string, system, quoteOnly bool, items *[]includeSearchPath) {
	if path == "" {
		return
	}
	path = filepath.Clean(path)
	*items = append(*items, includeSearchPath{Path: path, System: system, QuoteOnly: quoteOnly, ProvenanceKnown: true})
}

func normalizeCompileSearchPaths(paths []includeSearchPath) []includeSearchPath {
	systemPaths := make(map[string]struct{})
	for _, includePath := range paths {
		if includePath.System {
			systemPaths[includePath.Path] = struct{}{}
		}
	}
	result := make([]includeSearchPath, 0, len(paths))
	seenQuote := make(map[string]struct{})
	seenUser := make(map[string]struct{})
	seenSystem := make(map[string]struct{})
	for _, includePath := range paths {
		if includePath.Path == "" || !includePath.QuoteOnly {
			continue
		}
		if _, ok := seenQuote[includePath.Path]; ok {
			continue
		}
		seenQuote[includePath.Path] = struct{}{}
		result = append(result, includePath)
	}
	for _, includePath := range paths {
		if includePath.Path == "" || includePath.QuoteOnly || includePath.System {
			continue
		}
		if _, existsAsSystem := systemPaths[includePath.Path]; existsAsSystem {
			continue
		}
		if _, ok := seenUser[includePath.Path]; ok {
			continue
		}
		seenUser[includePath.Path] = struct{}{}
		result = append(result, includePath)
	}
	for _, includePath := range paths {
		if includePath.Path == "" || !includePath.System {
			continue
		}
		if _, ok := seenSystem[includePath.Path]; ok {
			continue
		}
		seenSystem[includePath.Path] = struct{}{}
		result = append(result, includePath)
	}
	return result
}

func mergeIncludeSearchPaths(existing, next []includeSearchPath) []includeSearchPath {
	if len(existing) == 0 {
		return append([]includeSearchPath(nil), next...)
	}
	merged := append([]includeSearchPath(nil), existing...)
	merged = append(merged, next...)
	return normalizeCompileSearchPaths(merged)
}

func sortedIncludeSearchPaths(paths map[string]includeSearchPath) []includeSearchPath {
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]includeSearchPath, 0, len(keys))
	for _, key := range keys {
		result = append(result, paths[key])
	}
	return result
}

func copySourceIncludeDirs(sourceIncludeDirs map[string][]includeSearchPath) map[string][]includeSearchPath {
	if len(sourceIncludeDirs) == 0 {
		return nil
	}
	result := make(map[string][]includeSearchPath, len(sourceIncludeDirs))
	for source, paths := range sourceIncludeDirs {
		result[source] = append([]includeSearchPath(nil), paths...)
	}
	return result
}
