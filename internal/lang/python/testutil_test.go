package python

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func writeNumberedTextFiles(t *testing.T, dir string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		mustWriteFile(t, filepath.Join(dir, "f-"+strconv.Itoa(i)+".txt"), "x")
	}
}
