package js

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"

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
}

func scanRepoEntry(ctx context.Context, state *scanRepoState, path string, entry fs.DirEntry) error {
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
