package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRunRejectsLocalFunctionLiteralCopy(t *testing.T) {
	root := t.TempDir()
	source := strings.Replace(copiedKeys, "func keys", "func outer() { keys := func", 1) + "; _ = keys }"
	testutil.MustWriteFile(t, filepath.Join(root, "internal", "lang", "copy.go"), source)
	var output bytes.Buffer
	code := run([]string{"-root", root}, &output, &output)
	if code != 1 || strings.Count(output.String(), "violation sorted-set-keys in outer.func: use shared.SortedKeys") != 1 {
		t.Fatalf("local collection closure: code=%d output=%s", code, &output)
	}
}
