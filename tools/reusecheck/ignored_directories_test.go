package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRunGoIgnoredDirectories(t *testing.T) {
	for _, tc := range []struct {
		directory, source, want string
		code                    int
	}{
		{directory: "_scratch", source: "not Go"},
		{directory: "nested/_scratch", source: "not Go"},
		{directory: "internal/lang/_archive", source: copiedKeys},
		{directory: "internal/lang/nested/_archive", source: copiedKeys},
		{directory: ".scratch", source: "not Go"},
		{directory: "internal/lang/nested/.archive", source: copiedKeys},
		{directory: "scratch", source: "not Go", code: 2, want: "expected 'package'"},
		{directory: "internal/lang/scratch_copy", source: copiedKeys, code: 1, want: "shared.SortedKeys"},
		{directory: "internal/lang/scratch.copy", source: copiedKeys, code: 1, want: "shared.SortedKeys"},
	} {
		t.Run(tc.directory, func(t *testing.T) {
			root := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(root, "valid.go"), "package fixture")
			testutil.MustWriteFile(t, filepath.Join(root, filepath.FromSlash(tc.directory), "source.go"), tc.source)
			var stdout, stderr bytes.Buffer
			code := run([]string{"-root", root}, &stdout, &stderr)
			output := stdout.String() + stderr.String()
			if code != tc.code || (tc.want == "" && output != "") || (tc.want != "" && !strings.Contains(output, tc.want)) {
				t.Fatalf("code=%d want=%d stdout=%s stderr=%s", code, tc.code, &stdout, &stderr)
			}
		})
	}
}

func TestScanSourcesDoesNotReadGoIgnoredDirectories(t *testing.T) {
	root := &fakeRoot{
		fileSystem: fstest.MapFS{
			"_scratch/bad.go":        {Data: []byte("not Go")},
			"nested/_scratch/bad.go": {Data: []byte("not Go")},
		},
		readErr: errors.New("ignored directory source was read"),
	}
	var stdout, stderr bytes.Buffer
	if code := scanRoot(root, &stdout, &stderr); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
}

func TestRunExplicitGoIgnoredRootStillScans(t *testing.T) {
	for _, name := range []string{"_scratch", ".scratch"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), name)
			testutil.MustWriteFile(t, filepath.Join(root, "internal", "lang", "fixture", "source.go"), copiedKeys)
			var stdout, stderr bytes.Buffer
			if code := run([]string{"-root", root}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "shared.SortedKeys") {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
		})
	}
}
