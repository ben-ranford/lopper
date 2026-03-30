package swift

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func (s *repoScanner) walkRepo(ctx context.Context) error {
	return shared.WalkRepoFiles(ctx, s.repoPath, 0, shouldSkipDir, s.visitRepoFile)
}

func (s *repoScanner) visitRepoFile(path string, entry fs.DirEntry) error {
	if !strings.EqualFold(filepath.Ext(entry.Name()), ".swift") {
		return nil
	}
	return s.scanSwiftFile(path, entry)
}

func (s *repoScanner) walk(ctx context.Context, path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if entry.IsDir() {
		return maybeSkipSwiftDir(entry.Name())
	}
	if !strings.EqualFold(filepath.Ext(entry.Name()), ".swift") {
		return nil
	}
	return s.scanSwiftFile(path, entry)
}

func (s *repoScanner) scanSwiftFile(path string, entry fs.DirEntry) error {
	s.foundSwift = true
	s.visited++
	if s.visited > maxScanFiles {
		return fs.SkipAll
	}
	if isLargeSwiftFile(entry) {
		s.skippedLargeFiles++
		return nil
	}

	content, err := safeio.ReadFileUnder(s.repoPath, path)
	if err != nil {
		return err
	}
	s.scan.Files = append(s.scan.Files, s.buildFileScan(path, entry.Name(), content))
	return nil
}

func isLargeSwiftFile(entry fs.DirEntry) bool {
	info, err := entry.Info()
	return err == nil && info.Size() > maxScannableSwiftFile
}

func (s *repoScanner) buildFileScan(path string, fallback string, content []byte) fileScan {
	relPath := s.relativePath(path, fallback)
	mappedImports := s.resolveImports(parseSwiftImports(content, relPath))
	return fileScan{
		Path:    relPath,
		Imports: mappedImports,
		Usage:   applyUnqualifiedUsageHeuristic(content, mappedImports, shared.CountUsage(content, mappedImports)),
	}
}

func (s *repoScanner) relativePath(path, fallback string) string {
	relPath, err := filepath.Rel(s.repoPath, path)
	if err != nil {
		return fallback
	}
	return relPath
}
