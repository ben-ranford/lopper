package js

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type scanRepoState struct {
	parser          *sourceParser
	repoPath        string
	result          *ScanResult
	parseErrorCount int
	parseErrorFiles []string
	oversizedCount  int
	oversizedFiles  []string
	excludedPaths   map[string]struct{}
}

func scanRepoEntry(ctx context.Context, state *scanRepoState, path string, entry fs.DirEntry) error {
	if isExcludedScanPath(state.excludedPaths, path) {
		if entry.IsDir() {
			return fs.SkipDir
		}
		return nil
	}
	if entry.IsDir() {
		if shared.ShouldSkipDir(entry.Name(), skipDirectories) {
			return fs.SkipDir
		}
		return nil
	}
	if !isSupportedFile(path) {
		return nil
	}

	content, tree, relPath, err := readAndParseFile(ctx, state.parser, state.repoPath, path)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			state.oversizedCount++
			oversizedPath := path
			if relPath, relErr := filepath.Rel(state.repoPath, path); relErr == nil {
				oversizedPath = relPath
			}
			if len(state.oversizedFiles) < 5 {
				state.oversizedFiles = append(state.oversizedFiles, oversizedPath)
			}
			return nil
		}
		return err
	}
	if tree.RootNode().HasError() {
		state.parseErrorCount++
		appendParseErrorFile(&state.parseErrorFiles, relPath)
	}
	state.result.Files = append(state.result.Files, analyzeFile(tree, content, relPath))
	return nil
}

func appendParseErrorFile(parseErrorFiles *[]string, relPath string) {
	if len(*parseErrorFiles) < 5 {
		*parseErrorFiles = append(*parseErrorFiles, relPath)
	}
}

func isExcludedScanPath(excludedPaths map[string]struct{}, path string) bool {
	if len(excludedPaths) == 0 {
		return false
	}
	_, excluded := excludedPaths[filepath.Clean(path)]
	return excluded
}

// excludedPathsForRepo resolves the analysis cache's directory/file
// exclusions into an absolute-path set scoped to repoPath, mirroring the
// PHP adapter's exclusion handling so cache-excluded inputs (for example the
// default runtime trace directory) do not affect fresh JS/TS scans.
func excludedPathsForRepo(repoPath string, directories, files []string) map[string]struct{} {
	if len(directories) == 0 && len(files) == 0 {
		return nil
	}
	repoPath = filepath.Clean(repoPath)
	paths := append(append([]string(nil), directories...), files...)
	excludedPaths := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		cleanedPath := filepath.Clean(path)
		relativePath, err := filepath.Rel(repoPath, cleanedPath)
		if err != nil || relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
			continue
		}
		excludedPaths[cleanedPath] = struct{}{}
	}
	return excludedPaths
}
