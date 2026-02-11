package jvm

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func writeNumberedTextFiles(t *testing.T, dir string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		writeFile(t, filepath.Join(dir, "f-"+strconv.Itoa(i)+".txt"), "x")
	}
}
