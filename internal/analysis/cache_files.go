package analysis

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

type cacheRelevantFile struct {
	absolutePath string
	relativePath string
}

func (c *analysisCache) collectRelevantFiles(rootPath string) ([]cacheRelevantFile, error) {
	files := make([]cacheRelevantFile, 0, 128)
	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, walkErr error) error {
		return collectRelevantFile(rootPath, path, d, walkErr, &files)
	})
	if err != nil {
		return nil, err
	}
	return files, nil
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
	switch base {
	case "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "package.json", "tsconfig.json", "composer.lock", "composer.json", "cargo.lock", "cargo.toml", "go.mod", "go.sum", "requirements.txt", "requirements-dev.txt", "pipfile", "pipfile.lock", "poetry.lock", "pyproject.toml", "pom.xml", "build.gradle", "build.gradle.kts", "gradle.lockfile", "settings.gradle", "settings.gradle.kts", "packages.lock.json", ".lopper.yml", ".lopper.yaml", "lopper.json":
		return true
	default:
		return false
	}
}
