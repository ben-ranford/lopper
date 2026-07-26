package js

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

type scanRepoWalkSummary = jsWalkSummary

func walkScanRepo(ctx context.Context, repoPath string, entryLimit int, visit func(path string, entry fs.DirEntry) error) (summary scanRepoWalkSummary, err error) {
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	root, err := openConstrainedRoot(repoPath)
	if err != nil {
		return summary, err
	}
	defer closeReadCloserPreserveErr(root, &err)

	budget := newJSWalkEntryBudget(entryLimit)
	summary, err = walkRootNoFollowContext(ctx, root, budget, scanRepoWalkFunc(repoPath, visit), nil)
	return summary, err
}

func scanRepoWalkFunc(repoPath string, visit func(path string, entry fs.DirEntry) error) rootWalkFunc {
	return func(relPath string, info fs.FileInfo) (bool, bool, error) {
		path := filepath.Join(repoPath, relPath)
		visitErr := visit(path, fs.FileInfoToDirEntry(info))
		switch {
		case errors.Is(visitErr, fs.SkipDir):
			return true, false, nil
		case errors.Is(visitErr, fs.SkipAll):
			return false, true, nil
		default:
			return false, false, visitErr
		}
	}
}

func jsRepoScanBudgetWarning(summary scanRepoWalkSummary) string {
	return fmt.Sprintf("stopped JS/TS repository scan after %d entries; analysis is partial", summary.entriesVisited)
}
