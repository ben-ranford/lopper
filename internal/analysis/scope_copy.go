package analysis

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const scopedCopyBufferSize = 32 * 1024

func copyFile(repoPath, scopedRoot, relativePath string) error {
	return copyFileWithContext(context.Background(), repoPath, scopedRoot, relativePath, maxScopedCopyBytes)
}

func copyFileWithContext(ctx context.Context, repoPath, scopedRoot, relativePath string, expectedSize int64) (err error) {
	if !isSafeRelativePath(relativePath) {
		return fmt.Errorf("invalid relative path for scoped copy: %s", relativePath)
	}
	cleanRelativePath := filepath.Clean(relativePath)
	targetPath := filepath.Join(scopedRoot, cleanRelativePath)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o750); err != nil {
		return err
	}

	sourceRoot, err := os.OpenRoot(repoPath)
	if err != nil {
		return fmt.Errorf("open source root: %w", err)
	}
	defer joinCloseError(&err, sourceRoot.Close)

	source, err := sourceRoot.Open(cleanRelativePath)
	if err != nil {
		return err
	}
	defer joinCloseError(&err, source.Close)
	sourceInfo, err := source.Stat()
	if err != nil {
		return err
	}
	if !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", errScopedCopyNonRegularFile, cleanRelativePath)
	}
	if sourceInfo.Size() > expectedSize {
		return fmt.Errorf("analysis scope copy source grew while copying %q: expected at most %d bytes, got %d", cleanRelativePath, expectedSize, sourceInfo.Size())
	}

	targetRoot, err := os.OpenRoot(scopedRoot)
	if err != nil {
		return fmt.Errorf("open target root: %w", err)
	}
	defer joinCloseError(&err, targetRoot.Close)

	target, err := targetRoot.OpenFile(cleanRelativePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer joinCloseError(&err, target.Close)
	written, err := copyScopedFileContents(ctx, target, io.LimitReader(source, expectedSize+1))
	if err != nil {
		return err
	}
	if written > expectedSize {
		return fmt.Errorf("analysis scope copy source grew while copying %q: expected at most %d bytes", cleanRelativePath, expectedSize)
	}
	return nil
}

func copyScopedFileContents(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	reader := scopeContextReader{
		check:  func() error { return scopeContextErr(ctx) },
		source: src,
	}
	return io.CopyBuffer(dst, &reader, make([]byte, scopedCopyBufferSize))
}

type scopeContextReader struct {
	check  func() error
	source io.Reader
}

func (r *scopeContextReader) Read(buffer []byte) (int, error) {
	if err := r.check(); err != nil {
		return 0, err
	}
	return r.source.Read(buffer)
}

func joinCloseError(target *error, closeFn func() error) {
	if closeErr := closeFn(); closeErr != nil {
		*target = errors.Join(*target, closeErr)
	}
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func isSafeRelativePath(relativePath string) bool {
	if filepath.IsAbs(relativePath) {
		return false
	}
	cleaned := filepath.Clean(relativePath)
	if cleaned == "." {
		return false
	}
	return cleaned != ".." && !strings.HasPrefix(cleaned, ".."+string(filepath.Separator))
}
