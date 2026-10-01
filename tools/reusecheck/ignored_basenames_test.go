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

func TestRunGoIgnoredBasenames(t *testing.T) {
	for _, tc := range []struct {
		name, source, want string
		code               int
	}{
		{name: ".backup.go", source: "not Go"},
		{name: "_scratch.go", source: "not Go"},
		{name: ".archive.go", source: copiedKeys},
		{name: "_archive.go", source: copiedKeys},
		{name: "backup.go", source: "not Go", code: 2, want: "expected 'package'"},
		{name: "scratch.go", source: "not Go", code: 2, want: "expected 'package'"},
		{name: "backup.copy.go", source: copiedKeys, code: 1, want: "shared.SortedKeys"},
		{name: "scratch_copy.go", source: copiedKeys, code: 1, want: "shared.SortedKeys"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "internal", "lang", "fixture")
			testutil.MustWriteFile(t, filepath.Join(directory, "valid.go"), "package fixture")
			testutil.MustWriteFile(t, filepath.Join(directory, tc.name), tc.source)
			var stdout, stderr bytes.Buffer
			code := run([]string{"-root", root}, &stdout, &stderr)
			output := stdout.String() + stderr.String()
			if code != tc.code || (tc.want == "" && output != "") || (tc.want != "" && !strings.Contains(output, tc.want)) {
				t.Fatalf("code=%d want=%d stdout=%s stderr=%s", code, tc.code, &stdout, &stderr)
			}
		})
	}
}

func TestScanSourcesDoesNotReadGoIgnoredFiles(t *testing.T) {
	root := &fakeRoot{
		fileSystem: fstest.MapFS{
			".backup.go":         {Data: []byte("not Go")},
			"_scratch.go":        {Data: []byte("not Go")},
			"nested/.backup.go":  {Data: []byte("not Go")},
			"nested/_scratch.go": {Data: []byte("not Go")},
		},
		readErr: errors.New("ignored source was read"),
	}
	var stdout, stderr bytes.Buffer
	if code := scanRoot(root, &stdout, &stderr); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
}
