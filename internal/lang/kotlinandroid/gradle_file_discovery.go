package kotlinandroid

import (
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type discoveredGradleFile struct {
	Path    string
	Content string
}

type gradleFileDiscoveryResult struct {
	Files    []discoveredGradleFile
	Warnings []string
	Matched  bool
}

func discoverBuildFiles(repoPath string, names ...string) (gradleFileDiscoveryResult, error) {
	return discoverGradleFiles(repoPath, func(fileName string) bool {
		return matchesBuildFile(fileName, names)
	})
}

func discoverGradleLockfiles(repoPath string) (gradleFileDiscoveryResult, error) {
	return discoverGradleFiles(repoPath, func(fileName string) bool {
		return strings.EqualFold(fileName, gradleLockfileName)
	})
}

func discoverGradleFiles(repoPath string, matches func(fileName string) bool) (gradleFileDiscoveryResult, error) {
	result := gradleFileDiscoveryResult{}
	walkErr := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !matches(entry.Name()) {
			return nil
		}
		result.Matched = true
		content, readErr := safeio.ReadFileUnder(repoPath, path)
		if readErr != nil {
			result.Warnings = append(result.Warnings, formatGradleReadWarning(repoPath, path, readErr))
			return nil
		}
		result.Files = append(result.Files, discoveredGradleFile{
			Path:    path,
			Content: string(content),
		})
		return nil
	})
	return result, walkErr
}

func matchesBuildFile(fileName string, names []string) bool {
	for _, name := range names {
		if strings.EqualFold(fileName, name) {
			return true
		}
	}
	return false
}
