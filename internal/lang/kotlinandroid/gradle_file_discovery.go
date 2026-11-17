package kotlinandroid

import (
	"context"
	"errors"
	"fmt"

	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
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

func streamBuildFiles(repoPath string, consume func(path, content string), names ...string) (gradleFileDiscoveryResult, error) {
	return streamGradleFiles(repoPath, func(fileName string) bool {
		return matchesBuildFile(fileName, names)
	}, consume)
}

func streamGradleLockfiles(repoPath string, consume func(path, content string)) (gradleFileDiscoveryResult, error) {
	return streamGradleFiles(repoPath, func(fileName string) bool {
		return strings.EqualFold(fileName, gradleLockfileName)
	}, consume)
}

// detachGradleDescriptors prevents parsed substrings from retaining a whole input file.
func detachGradleDescriptors(items []dependencyDescriptor) []dependencyDescriptor {
	for i := range items {
		items[i].Name = strings.Clone(items[i].Name)
		items[i].Group = strings.Clone(items[i].Group)
		items[i].Artifact = strings.Clone(items[i].Artifact)
		items[i].Version = strings.Clone(items[i].Version)
	}
	return items
}

func streamGradleFiles(repoPath string, matches func(fileName string) bool, consume func(path, content string)) (gradleFileDiscoveryResult, error) {
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
		consume(path, string(content))
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

// discoverBuildFiles preserves the materialized stage interface for callers that
// explicitly need snapshots. Production dependency collection uses streamBuildFiles.
func discoverBuildFiles(repoPath string, names ...string) (gradleFileDiscoveryResult, error) {
	var files []discoveredGradleFile
	result, err := streamBuildFiles(repoPath, func(path, content string) {
		files = append(files, discoveredGradleFile{Path: path, Content: content})
	}, names...)
	result.Files = files
	return result, err
}

func discoverGradleLockfiles(repoPath string) (gradleFileDiscoveryResult, error) {
	var files []discoveredGradleFile
	result, err := streamGradleLockfiles(repoPath, func(path, content string) {
		files = append(files, discoveredGradleFile{Path: path, Content: content})
	})
	result.Files = files
	return result, err
}

func streamGradleFilesContext(ctx context.Context, repoPath string, root safeio.Root, bytes *shared.GradleDiscoveryBudget, consume func(string, []byte) error) error {
	return streamGradleFilesContextWithWarnings(ctx, repoPath, root, bytes, nil, consume)
}

func streamGradleFilesContextWithWarnings(ctx context.Context, repoPath string, root safeio.Root, bytes *shared.GradleDiscoveryBudget, warnings *[]string, consume func(string, []byte) error) error {
	if err := ctx.Err(); err != nil {
		return shared.GradleDiscoveryFailure(repoPath, "discovery", err)
	}
	// Legacy WalkDir applies the skip predicate to the root itself as well.
	if shouldSkipDir(filepath.Base(repoPath)) {
		return nil
	}
	matches := func(name string) bool {
		return matchesBuildFile(name, []string{buildGradleName, buildGradleKTSName, gradleLockfileName})
	}
	budget := shared.RootedWalkBudget{MaxTraversalEntries: 8192, MaxFiles: 2048, MaxWorkItems: 2048, CountCandidate: func(_ string, entry fs.DirEntry) bool { return matches(entry.Name()) }}
	err := shared.WalkRepoFilesWithinRootPinned(ctx, repoPath, root, budget, shouldSkipDir, func(file shared.RootedWalkFile) error {
		if !matches(file.Leaf) {
			return nil
		}
		content, warning, err := readGradleInputWithinRoot(ctx, repoPath, file, bytes)
		if err != nil {
			return err
		}
		if warning != "" {
			if warnings != nil {
				*warnings = append(*warnings, warning)
			}
			return nil
		}
		if err := consume(file.Path, content); err != nil {
			return err
		}
		return ctx.Err()
	})
	return shared.GradleDiscoveryWalkFailure(repoPath, budget, errors.Join(err, ctx.Err()))
}

func readGradleInputWithinRoot(ctx context.Context, repoPath string, file shared.RootedWalkFile, bytes *shared.GradleDiscoveryBudget) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", shared.GradleDiscoveryFailure(file.Path, "read", err)
	}
	entryType := file.Entry.Type()
	if entryType&fs.ModeType != 0 {
		readErr := fs.ErrInvalid
		if entryType&fs.ModeSymlink != 0 {
			readErr = fmt.Errorf("%w: %s", safeio.ErrTargetPathSymlink, file.Leaf)
		}
		return nil, formatGradleReadWarning(repoPath, file.Path, readErr), nil
	}
	content, err := bytes.ReadWithinRoot(ctx, file.Parent, file.Leaf, file.Path)
	return content, "", err
}
