package js

import (
	"context"
	"errors"
	"io/fs"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type scanRepoState struct {
	parser            *sourceParser
	repoPath          string
	result            *ScanResult
	skippedLargeFiles int
	skippedSymlinks   int
	parseErrorCount   int
	parseErrorFiles   []string
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
	if entry.Type()&fs.ModeSymlink != 0 {
		state.skippedSymlinks++
		return nil
	}

	content, tree, relPath, err := readAndParseFile(ctx, state.parser, state.repoPath, path)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			state.skippedLargeFiles++
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
