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

var errPHPShortOpenTagCacheTraversalLimit = errors.New("php short_open_tag cache traversal limit exceeded")

func (c *analysisCache) collectRelevantFiles(rootPath string) ([]cacheRelevantFile, error) {
	return c.collectRelevantFilesWithExcludedPaths(rootPath, c.cacheExcludedPaths(rootPath, Request{}))
}

func (c *analysisCache) collectRelevantFilesWithExcludedPaths(rootPath string, excludedPaths []string) ([]cacheRelevantFile, error) {
	files := make([]cacheRelevantFile, 0, 128)
	excludedDirectories := cacheExcludedDirectorySet(excludedPaths)
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		return collectRelevantFileWithExcludedPaths(rootPath, path, d, walkErr, excludedDirectories, &files)
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (c *analysisCache) cacheExcludedPaths(rootPath string, req Request) []string {
	rootPath = filepath.Clean(rootPath)
	paths := make(map[string]struct{})
	if c != nil && strings.TrimSpace(c.options.Path) != "" {
		addCacheExcludedPath(paths, rootPath, c.options.Path)
	}
	if tracePath := strings.TrimSpace(req.RuntimeTracePath); tracePath != "" {
		addCacheExcludedPath(paths, rootPath, filepath.Dir(tracePath))
	} else if strings.TrimSpace(req.RuntimeTestCommand) != "" {
		addCacheExcludedPath(paths, rootPath, filepath.Dir(runtime.DefaultTracePath(rootPath)))
	}
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

func cacheExcludedDirectorySet(paths []string) map[string]struct{} {
	if len(paths) == 0 {
		return nil
	}
	directories := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			directories[filepath.Clean(path)] = struct{}{}
		}
	}
	return directories
}

func collectPHPShortOpenTagTraversalEntries(rootPath string, excludedPaths []string) ([]cacheTraversalEntry, error) {
	rootPath = filepath.Clean(rootPath)
	entries := make([]cacheTraversalEntry, 0, shared.PHPShortOpenTagConfigWalkEntryLimit+1)
	excludedDirectories := cacheExcludedDirectorySet(excludedPaths)
	visited := 0
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == rootPath {
			return nil
		}
		if _, excluded := excludedDirectories[path]; excluded {
			return filepath.SkipDir
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

func collectRelevantFileWithExcludedPaths(rootPath, path string, d fs.DirEntry, walkErr error, excludedDirectories map[string]struct{}, files *[]cacheRelevantFile) error {
	if walkErr != nil {
		return walkErr
	}
	if _, excluded := excludedDirectories[path]; excluded {
		return filepath.SkipDir
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
