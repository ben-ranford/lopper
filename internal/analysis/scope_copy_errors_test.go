package analysis

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

const scopeKeepJS = "keep.js"

func TestScopeCopyFileAdditionalErrorBranches(t *testing.T) {
	repo := t.TempDir()
	scopedRoot := t.TempDir()
	sourcePath := filepath.Join(repo, "src", scopeKeepJS)
	writeScopeFile(t, sourcePath, "export const keep = true\n")

	targetDir := filepath.Join(scopedRoot, "src", scopeKeepJS)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	sourceInfo, statErr := os.Stat(sourcePath)
	if statErr != nil {
		t.Fatalf("stat source path: %v", statErr)
	}
	if err := copyFile(context.Background(), repo, scopedRoot, filepath.Join("src", scopeKeepJS), sourceInfo.Size()); err == nil {
		t.Fatalf("expected copyFile to fail when target path is a directory")
	}

	sourceDir := filepath.Join(repo, "src", "nested")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source dir: %v", err)
	}
	if err := copyFile(context.Background(), repo, scopedRoot, filepath.Join("src", "nested"), 0); err == nil {
		t.Fatalf("expected copyFile to fail when source path is a directory")
	}

	var err error
	joinCloseError(&err, func() error { return errors.New("close failed") })
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("expected joinCloseError to propagate close failure, got %v", err)
	}
}

func TestCopyFileHonorsCanceledContextBeforeOpen(t *testing.T) {
	repo := t.TempDir()
	scopedRoot := t.TempDir()
	sourcePath := filepath.Join(repo, "src", scopeKeepJS)
	writeScopeFile(t, sourcePath, "export const keep = true\n")

	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatalf("stat source path: %v", err)
	}
	if err := copyFile(testutil.CanceledContext(), repo, scopedRoot, filepath.Join("src", scopeKeepJS), sourceInfo.Size()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context error, got %v", err)
	}
}

func TestCopyFileRejectsSourceGrowthBeyondReservedSize(t *testing.T) {
	repo := t.TempDir()
	scopedRoot := t.TempDir()
	sourcePath := filepath.Join(repo, "src", scopeKeepJS)
	content := "export const keep = true\n"
	writeScopeFile(t, sourcePath, content)

	if err := copyFile(context.Background(), repo, scopedRoot, filepath.Join("src", scopeKeepJS), int64(len(content)-1)); err == nil || !strings.Contains(err.Error(), "source grew while copying") {
		t.Fatalf("expected source growth error, got %v", err)
	}
}

func TestCopyScopedFileContentsErrorBranches(t *testing.T) {
	readErr := errors.New("read failed")
	if _, err := copyScopedFileContents(context.Background(), io.Discard, &errorReader{err: readErr}); !errors.Is(err, readErr) {
		t.Fatalf("expected read error, got %v", err)
	}

	if _, err := copyScopedFileContents(context.Background(), &shortWriter{}, strings.NewReader("abc")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("expected short write error, got %v", err)
	}

	writeErr := errors.New("write failed")
	if _, err := copyScopedFileContents(context.Background(), &failingWriter{err: writeErr}, strings.NewReader("abc")); !errors.Is(err, writeErr) {
		t.Fatalf("expected write error, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reader := cancelingReader{
		cancel: cancel,
		data:   []byte("abc"),
	}
	if _, err := copyScopedFileContents(ctx, io.Discard, &reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected mid-copy cancellation, got %v", err)
	}
}

type errorReader struct {
	err error
}

func (r *errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type shortWriter struct{}

func (*shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return len(p) - 1, nil
}

type failingWriter struct {
	err error
}

func (w *failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type cancelingReader struct {
	cancel func()
	data   []byte
	read   bool
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	copy(p, r.data)
	r.cancel()
	return len(r.data), nil
}
