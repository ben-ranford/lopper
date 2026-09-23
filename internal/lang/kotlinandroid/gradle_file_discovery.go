package kotlinandroid

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const gradleDiscoveryContentByteLimit int64 = 16 * 1024 * 1024

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

func collectGradleFileDescriptorsWithWarnings(repoPath string, discover func(string) (gradleFileDiscoveryResult, error), parser func([]discoveredGradleFile) ([]dependencyDescriptor, []string), scanTarget string) ([]dependencyDescriptor, bool, []string) {
	discovery, walkErr := discover(repoPath)
	descriptors, parseWarnings := parser(discovery.Files)
	warnings := append([]string{}, discovery.Warnings...)
	warnings = append(warnings, parseWarnings...)
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("unable to scan %s: %v", scanTarget, walkErr))
	}
	return descriptors, discovery.Matched, shared.DedupeWarnings(warnings)
}

func discoverGradleFiles(repoPath string, matches func(fileName string) bool) (gradleFileDiscoveryResult, error) {
	result := gradleFileDiscoveryResult{}
	var retainedBytes int64
	budgetWarningAdded := false
	addBudgetWarning := func() {
		if budgetWarningAdded {
			return
		}
		result.Warnings = append(result.Warnings, fmt.Sprintf("stopped reading Gradle files after reaching the %d-byte content limit", gradleDiscoveryContentByteLimit))
		budgetWarningAdded = true
	}
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
		remainingBytes := gradleDiscoveryContentByteLimit - retainedBytes
		if remainingBytes <= 0 {
			addBudgetWarning()
			return nil
		}
		readLimit := int64(shared.GradleManifestByteLimit)
		if remainingBytes < readLimit {
			readLimit = remainingBytes
		}
		content, readErr := safeio.ReadFileUnderLimit(repoPath, path, readLimit)
		if readErr != nil {
			if remainingBytes < shared.GradleManifestByteLimit && errors.Is(readErr, safeio.ErrFileTooLarge) {
				addBudgetWarning()
				return nil
			}
			result.Warnings = append(result.Warnings, formatGradleReadWarning(repoPath, path, readErr))
			return nil
		}
		retainedBytes += int64(len(content))
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
