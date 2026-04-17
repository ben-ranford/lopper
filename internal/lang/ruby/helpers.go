package ruby

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

func walkRubyRepoFiles(ctx context.Context, repoPath string, visitFile func(path string, entry fs.DirEntry) error) error {
	return filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		return visitFile(path, entry)
	})
}

func normalizeDependencyID(value string) string {
	value = shared.NormalizeDependencyID(value)
	value = strings.ReplaceAll(value, "_", "-")
	return strings.ReplaceAll(value, ".", "-")
}

func shouldSkipDir(name string) bool {
	if shared.ShouldSkipCommonDir(name) {
		return true
	}
	return rubySkippedDirs[strings.ToLower(name)]
}
