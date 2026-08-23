package analysis

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/runtime"
)

type cacheRelevantFile struct {
	absolutePath string
	relativePath string
}

type cacheTraversalEntry struct {
	relativePath string
	kind         string
}

type cacheAnalysisExclusions struct {
	directories []string
	files       []string
}

var errPHPShortOpenTagCacheTraversalLimit = errors.New("php short_open_tag cache traversal limit exceeded")

func (c *analysisCache) collectRelevantFiles(rootPath string) ([]cacheRelevantFile, error) {
	return c.collectRelevantFilesWithExclusions(rootPath, c.cacheAnalysisExclusions(rootPath, Request{}))
}

func (c *analysisCache) collectRelevantFilesWithExclusions(rootPath string, exclusions cacheAnalysisExclusions) ([]cacheRelevantFile, error) {
	files := make([]cacheRelevantFile, 0, 128)
	excludedPaths := cacheExcludedPathSet(exclusions)
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		return collectRelevantFileWithExcludedPaths(rootPath, path, d, walkErr, excludedPaths, &files)
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (c *analysisCache) cacheAnalysisExclusions(rootPath string, req Request) cacheAnalysisExclusions {
	rootPath = filepath.Clean(rootPath)
	directories := make(map[string]struct{})
	files := make(map[string]struct{})
	if c != nil && strings.TrimSpace(c.options.Path) != "" {
		addCacheExcludedPath(directories, rootPath, c.options.Path)
	}
	if tracePath := strings.TrimSpace(req.RuntimeTracePath); tracePath != "" {
		tracePath = runtimeTracePathForRepo(rootPath, tracePath)
		addCacheExcludedPath(files, rootPath, tracePath)
		addCacheExcludedPath(files, rootPath, runtime.TraceStatePath(tracePath))
	} else if strings.TrimSpace(req.RuntimeTestCommand) != "" {
		addCacheExcludedPath(directories, rootPath, filepath.Dir(runtime.DefaultTracePath(rootPath)))
	}
	return cacheAnalysisExclusions{
		directories: sortedCacheExcludedPaths(directories),
		files:       sortedCacheExcludedPaths(files),
	}
}

func runtimeTracePathForRepo(rootPath, tracePath string) string {
	tracePath = filepath.Clean(strings.TrimSpace(tracePath))
	if filepath.IsAbs(tracePath) {
		return tracePath
	}
	return filepath.Join(rootPath, tracePath)
}

func sortedCacheExcludedPaths(paths map[string]struct{}) []string {
	if len(paths) == 0 {
		return nil
	}
	excludedPaths := make([]string, 0, len(paths))
	for path := range paths {
		excludedPaths = append(excludedPaths, path)
	}
	sort.Strings(excludedPaths)
	return excludedPaths
}

func addCacheExcludedPath(paths map[string]struct{}, rootPath, candidatePath string) {
	candidatePath = filepath.Clean(strings.TrimSpace(candidatePath))
	relativePath, err := filepath.Rel(rootPath, candidatePath)
	if err != nil || relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return
	}
	paths[candidatePath] = struct{}{}
}

func cacheExcludedPathSet(exclusions cacheAnalysisExclusions) map[string]struct{} {
	if len(exclusions.directories) == 0 && len(exclusions.files) == 0 {
		return nil
	}
	paths := append(append([]string(nil), exclusions.directories...), exclusions.files...)
	excludedPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			excludedPaths[filepath.Clean(path)] = struct{}{}
		}
	}
	return excludedPaths
}

func collectPHPShortOpenTagTraversalEntries(rootPath string, exclusions cacheAnalysisExclusions) ([]cacheTraversalEntry, error) {
	rootPath = filepath.Clean(rootPath)
	entries := make([]cacheTraversalEntry, 0, shared.PHPShortOpenTagConfigWalkEntryLimit+1)
	excludedPaths := cacheExcludedPathSet(exclusions)
	visited := 0
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == rootPath {
			return nil
		}
		if _, excluded := excludedPaths[path]; excluded {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && shouldSkipPHPShortOpenTagConfigDir(path, d.Name()) {
			return filepath.SkipDir
		}
		visited++
		relativePath, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		kind := "file"
		if d.IsDir() {
			kind = "dir"
		}
		entries = append(entries, cacheTraversalEntry{
			relativePath: filepath.ToSlash(relativePath),
			kind:         kind,
		})
		if visited > shared.PHPShortOpenTagConfigWalkEntryLimit {
			return errPHPShortOpenTagCacheTraversalLimit
		}
		return nil
	})
	if errors.Is(err, errPHPShortOpenTagCacheTraversalLimit) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func collectRelevantFileWithExcludedPaths(rootPath, path string, d fs.DirEntry, walkErr error, excludedPaths map[string]struct{}, files *[]cacheRelevantFile) error {
	if walkErr != nil {
		return walkErr
	}
	if _, excluded := excludedPaths[path]; excluded {
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	return collectRelevantFile(rootPath, path, d, nil, files)
}

func shouldSkipPHPShortOpenTagConfigDir(path, name string) bool {
	switch name {
	case ".git", ".idea", ".lopper-cache", "node_modules", "vendor", "dist", "build", ".next", ".turbo", "coverage", "tmp", "cache":
		return true
	default:
		_, err := os.Stat(filepath.Join(path, "composer.json"))
		return err == nil
	}
}

func collectRelevantFile(rootPath, path string, d fs.DirEntry, walkErr error, files *[]cacheRelevantFile) error {
	if walkErr != nil {
		return walkErr
	}
	if path == rootPath {
		return nil
	}
	if shouldSkipDirEntry(d) {
		return filepath.SkipDir
	}
	if !shouldHashFile(path, d) {
		return nil
	}
	record, err := buildRelevantFile(rootPath, path)
	if err != nil {
		return err
	}
	*files = append(*files, record)
	return nil
}

func shouldSkipDirEntry(d fs.DirEntry) bool {
	return d.IsDir() && shouldSkipCacheDir(d.Name())
}

func shouldHashFile(path string, d fs.DirEntry) bool {
	return d.Type().IsRegular() && isCacheRelevantFile(path)
}

func buildRelevantFile(rootPath, path string) (cacheRelevantFile, error) {
	rel, err := filepath.Rel(rootPath, path)
	if err != nil {
		return cacheRelevantFile{}, err
	}
	return cacheRelevantFile{
		absolutePath: path,
		relativePath: filepath.ToSlash(rel),
	}, nil
}

func shouldSkipCacheDir(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == ".lopper-cache" {
		return true
	}
	return shared.ShouldSkipCommonDir(normalized)
}

func isCacheRelevantFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if lockOrConfigFile(base) {
		return true
	}
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".py", ".go", ".rs", ".php", ".java", ".kt", ".kts", ".cs", ".fs", ".fsx", ".c", ".cc", ".cpp", ".cxx", ".h", ".hpp":
		return true
	default:
		return false
	}
}

func lockOrConfigFile(base string) bool {
	if shared.IsGradleVersionCatalogFile(base) {
		return true
	}
	switch base {
	case "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "package.json", "tsconfig.json", "composer.lock", "composer.json", "php.ini", ".user.ini", ".htaccess", "cargo.lock", "cargo.toml", "go.mod", "go.sum", "requirements.txt", "requirements-dev.txt", "pipfile", "pipfile.lock", "poetry.lock", "pyproject.toml", "uv.lock", "pom.xml", "build.gradle", "build.gradle.kts", "gradle.lockfile", "settings.gradle", "settings.gradle.kts", "packages.lock.json", ".lopper.yml", ".lopper.yaml", "lopper.json":
		return true
	default:
		return false
	}
}
