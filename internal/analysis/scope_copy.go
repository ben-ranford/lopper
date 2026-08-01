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

func copyFile(ctx context.Context, repoPath, scopedRoot, relativePath string, expectedSize int64) (err error) {
	if !isSafeRelativePath(relativePath) {
		return fmt.Errorf("invalid relative path for scoped copy: %s", relativePath)
	}
	if err := scopeContextErr(ctx); err != nil {
		return err
	}
	cleanRelativePath := filepath.Clean(relativePath)
	sourcePath := filepath.Join(repoPath, cleanRelativePath)
	targetPath := filepath.Join(scopedRoot, cleanRelativePath)
	if !pathWithin(repoPath, sourcePath) {
		return fmt.Errorf("source path escapes repository scope: %s", sourcePath)
	}
	if !pathWithin(scopedRoot, targetPath) {
		return fmt.Errorf("target path escapes scoped workspace: %s", targetPath)
	}
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
	buffer := make([]byte, scopedCopyBufferSize)
	var written int64
	for {
		if err := scopeContextErr(ctx); err != nil {
			return written, err
		}
		readBytes, readErr := src.Read(buffer)
		if readBytes > 0 {
			wroteBytes, writeErr := dst.Write(buffer[:readBytes])
			written += int64(wroteBytes)
			if writeErr != nil {
				return written, writeErr
			}
			if wroteBytes != readBytes {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
	}
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
