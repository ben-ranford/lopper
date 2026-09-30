package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRunRejectsFunctionLiteralCopy(t *testing.T) {
	for _, tc := range []struct{ prefix, suffix, owner string }{
		{"func outer() { keys := func", "; _ = keys }", "outer.func"},
		{`var builders = map[string]func(map[string]struct{}) []string{"keys": func`, "}", "builders.func"},
	} {
		t.Run(tc.owner, func(t *testing.T) {
			root := t.TempDir()
			source := strings.Replace(copiedKeys, "func keys", tc.prefix, 1) + tc.suffix
			testutil.MustWriteFile(t, filepath.Join(root, "internal", "lang", "copy.go"), source)
			var output bytes.Buffer
			code := run([]string{"-root", root}, &output, &output)
			if code != 1 || strings.Count(output.String(), "violation sorted-set-keys in "+tc.owner+": use shared.SortedKeys") != 1 {
				t.Fatalf("collection closure: code=%d output=%s", code, &output)
			}
		})
	}
}
