package shared

import (
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/safeio"
)

// ReadSourceFile reads a source confined to repoPath and returns its display
// path. When the supplied paths cannot be relativized (such as an absolute repo
// and relative source), the display path remains sourcePath. Read errors return
// neither partial content nor a display path.
func ReadSourceFile(repoPath, sourcePath string) ([]byte, string, error) {
	content, err := safeio.ReadFileUnder(repoPath, sourcePath)
	if err != nil {
		return nil, "", err
	}
	relativePath, err := filepath.Rel(repoPath, sourcePath)
	if err != nil {
		relativePath = sourcePath
	}
	return content, relativePath, nil
}
