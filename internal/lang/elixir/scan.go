package elixir

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func scanElixirRepo(ctx context.Context, repoPath string, declared map[string]struct{}) (scanResult, error) {
	result := scanResult{declared: declared}
	err := shared.WalkRepoFiles(ctx, repoPath, maxScanFiles, shouldSkipDir, func(path string, _ os.DirEntry) error {
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".ex" && ext != ".exs" {
			return nil
		}
		content, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repoPath, path)
		if err != nil {
			relative = path
		}
		imports := parseImports(content, relative, declared)
		result.files = append(result.files, shared.FileUsage{
			Imports: imports,
			Usage:   shared.CountUsage(content, imports),
		})
		return nil
	})
	return result, err
}
