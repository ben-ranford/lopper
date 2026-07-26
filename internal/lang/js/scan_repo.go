package js

import (
	"context"
	"errors"
	"io/fs"
	"sort"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
	sitter "github.com/smacker/go-tree-sitter"
)

type scanRepoState struct {
	parser                 *sourceParser
	repoPath               string
	result                 *ScanResult
	readAndParseFile       func(context.Context, *sourceParser, string, string) ([]byte, *sitter.Tree, string, error)
	skippedLargeFiles      int
	skippedNonRegularFiles int
	parseErrorCount        int
	parseErrorFiles        []string
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

	readAndParse := state.readAndParseFile
	if readAndParse == nil {
		readAndParse = readAndParseFile
	}
	content, tree, relPath, err := readAndParse(ctx, state.parser, state.repoPath, path)
	if err != nil {
		if errors.Is(err, safeio.ErrFileTooLarge) {
			state.skippedLargeFiles++
			return nil
		}
		if isPureNonRegularReadError(err) {
			state.skippedNonRegularFiles++
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
	*parseErrorFiles = append(*parseErrorFiles, relPath)
	sort.Strings(*parseErrorFiles)
	if len(*parseErrorFiles) > 5 {
		*parseErrorFiles = (*parseErrorFiles)[:5]
	}
}

type Unwrapper interface {
	Unwrap() error
}

type UnwrapErrorser interface {
	Unwrap() []error
}

func isPureNonRegularReadError(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(UnwrapErrorser); ok {
		unwrapped := joined.Unwrap()
		if len(unwrapped) == 0 {
			return false
		}
		for _, innerErr := range unwrapped {
			if !isPureNonRegularReadError(innerErr) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(Unwrapper); ok {
		innerErr := wrapped.Unwrap()
		if innerErr == nil {
			return errors.Is(err, safeio.ErrNonRegularFile)
		}
		return isPureNonRegularReadError(innerErr)
	}
	return errors.Is(err, safeio.ErrNonRegularFile) || errors.Is(err, safeio.ErrTargetPathSymlink)
}
