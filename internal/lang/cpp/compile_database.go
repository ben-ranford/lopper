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
	SourceFiles        []string
	Warnings           []string
}

type compileContextCollector struct {
	repoPath      string
	includeDirSet map[string]struct{}
	sourceFileSet map[string]struct{}
	warnings      []string
	visited       int
	found         bool
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
		repoPath:      repoPath,
		includeDirSet: make(map[string]struct{}),
		sourceFileSet: make(map[string]struct{}),
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

	warnings, err := collectCompileDatabase(path, c.repoPath, c.includeDirSet, c.sourceFileSet)
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
		SourceFiles:        shared.SortedKeys(c.sourceFileSet),
		Warnings:           append([]string(nil), c.warnings...),
	}
	if !result.HasCompileDatabase {
		result.Warnings = append(result.Warnings, "compile_commands.json not found; using include-graph heuristics without translation unit context")
	}
	return result
}

func collectCompileDatabase(path, repoPath string, includeDirSet, sourceFileSet map[string]struct{}) ([]string, error) {
	entries, warnings, err := readCompileDatabase(path, repoPath)
	if err != nil || len(entries) == 0 {
		return warnings, err
	}

	for _, entry := range entries {
		baseDir := resolveCompileDirectory(path, entry.Directory)
		recordCompileSource(sourceFileSet, resolveCompilePath(baseDir, entry.File))
		recordCompileIncludes(includeDirSet, entry.compileArgs(), baseDir)
	}
	return warnings, nil
}

func readCompileDatabase(path, repoPath string) ([]compileCommandEntry, []string, error) {
	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return nil, nil, err
	}

	var entries []compileCommandEntry
	if err := json.Unmarshal(content, &entries); err != nil {
		return nil, []string{fmt.Sprintf("failed to parse %s: %v", relOrBase(repoPath, path), err)}, nil
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

func recordCompileIncludes(includeDirSet map[string]struct{}, args []string, baseDir string) {
	for _, includeDir := range extractIncludeDirs(args, baseDir) {
		if includeDir != "" {
			includeDirSet[includeDir] = struct{}{}
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
