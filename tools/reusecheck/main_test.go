package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ben-ranford/lopper/internal/testutil"
)

const copiedKeys = `package fixture
import "sort"
func keys(values map[string]struct{}) []string {
 if len(values)==0{return nil}
 items:=make([]string,0,len(values))
 for value:=range values {items=append(items,value)}
 sort.Strings(items)
 return items
}`

type fakeRoot struct {
	fileSystem fs.FS
	readErr    error
	closeErr   error
}

func (r *fakeRoot) FileSystem() fs.FS { return r.fileSystem }

func (r *fakeRoot) ReadFile(name string) ([]byte, error) {
	if r.readErr != nil {
		return nil, r.readErr
	}
	return fs.ReadFile(r.fileSystem, name)
}

func (r *fakeRoot) Close() error { return r.closeErr }

type walkErrorFS struct{}

func (*walkErrorFS) Open(string) (fs.File, error) { return nil, errors.New("walk unavailable") }

func TestRunRejectsCopyAndParseFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "internal", "lang", "fixture", "copy.go")
	testutil.MustWriteFile(t, path, copiedKeys)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 1 || !strings.Contains(stdout.String(), "shared.SortedKeys") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
	testutil.MustWriteFile(t, path, "package fixture\nfunc broken")
	if code := run([]string{"-root", root}, &stdout, &stderr); code != 2 {
		t.Fatalf("parse error code=%d", code)
	}
}

func TestRunBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"unknown flag", []string{"--unknown"}, 2},
		{"positional", []string{"unexpected"}, 2},
		{"missing root", []string{"-root", filepath.Join(t.TempDir(), "missing")}, 2},
		{"missing exceptions directory", []string{"-exceptions", filepath.Join(t.TempDir(), "missing", "exceptions.json")}, 2},
		{"missing exceptions file", []string{"-exceptions", filepath.Join(t.TempDir(), "missing.json")}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			if code := run(tc.args, &output, &output); code != tc.code {
				t.Fatalf("code=%d output=%s", code, &output)
			}
		})
	}
	root := t.TempDir()
	for _, name := range []string{"vendor/bad.go", ".git/bad.go", "testdata/bad.go", "node_modules/bad.go", "ignored_test.go", "readme.txt"} {
		testutil.MustWriteFile(t, filepath.Join(root, name), "not Go")
	}
	testutil.MustWriteFile(t, filepath.Join(root, "valid.go"), "package valid")
	var output bytes.Buffer
	if code := run([]string{"-root", root}, &output, &output); code != 0 {
		t.Fatalf("code=%d output=%s", code, &output)
	}
}

func TestRunRejectsSourceRootEscape(t *testing.T) {
	root := t.TempDir()
	outsideFile := filepath.Join(t.TempDir(), "outside.go")
	testutil.MustWriteFile(t, outsideFile, "package outside")
	if err := os.Symlink(outsideFile, filepath.Join(root, "escape.go")); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := run([]string{"-root", root}, &output, &output); code != 2 || !strings.Contains(output.String(), "escapes") {
		t.Fatalf("root escape code=%d output=%s", code, &output)
	}
}

type failedWriter struct{}

func (*failedWriter) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }
func TestRunFailsWhenFindingCannotBeReported(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(root, "internal", "lang", "copy.go"), copiedKeys)
	var stderr bytes.Buffer
	if code := run([]string{"-root", root}, &failedWriter{}, &stderr); code != 2 || !strings.Contains(stderr.String(), "output unavailable") {
		t.Fatalf("code=%d stderr=%s", code, &stderr)
	}
}

func TestRunStillFailsWhenErrorOutputFails(t *testing.T) {
	for _, args := range [][]string{{"unexpected"}, {"-root", filepath.Join(t.TempDir(), "missing")}, {"-legacy-advisory=true"}} {
		if code := run(args, &failedWriter{}, &failedWriter{}); code != 2 {
			t.Fatalf("code=%d args=%v", code, args)
		}
	}
}

func TestMainDelegatesExitCode(t *testing.T) {
	previousArgs, previousExit := os.Args, processExit
	t.Cleanup(func() {
		os.Args = previousArgs
		processExit = previousExit
	})
	os.Args = []string{"reusecheck", "-root", t.TempDir()}
	var exitCode int
	processExit = func(code int) { exitCode = code }
	main()
	if exitCode != 0 {
		t.Fatalf("exit code=%d", exitCode)
	}
}

func TestScanRootReportsWalkReadAndCloseFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		root   *fakeRoot
		stderr *failedWriter
		want   string
	}{
		{name: "walk", root: &fakeRoot{fileSystem: &walkErrorFS{}}, want: "walk unavailable"},
		{name: "walk output", root: &fakeRoot{fileSystem: &walkErrorFS{}}, stderr: &failedWriter{}},
		{name: "read", root: &fakeRoot{fileSystem: fstest.MapFS{"bad.go": {Data: []byte("package bad")}}, readErr: errors.New("read unavailable")}, want: "read unavailable"},
		{name: "close", root: &fakeRoot{fileSystem: fstest.MapFS{}, closeErr: errors.New("close unavailable")}, want: "close unavailable"},
		{name: "close output", root: &fakeRoot{fileSystem: fstest.MapFS{}, closeErr: errors.New("close unavailable")}, stderr: &failedWriter{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			errorOutput := io.Writer(&stderr)
			if tc.stderr != nil {
				errorOutput = tc.stderr
			}
			code := scanRoot(tc.root, &stdout, errorOutput)
			if code != 2 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
			if tc.want != "" && !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr=%s, want %q", &stderr, tc.want)
			}
		})
	}
}

func TestRootHandleReadsRelativeAndAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(root, "file.go"), "package fixture")
	opened, err := openSourceRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	handle := &rootHandle{root: opened}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, path := range []string{"file.go", filepath.Join(root, "file.go")} {
		data, err := handle.ReadFile(path)
		if err != nil || string(data) != "package fixture" {
			t.Fatalf("path=%s data=%s error=%v", path, data, err)
		}
	}
}

func TestRootPathResolutionErrors(t *testing.T) {
	relativeRoot, err := os.OpenRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := relativeRoot.Close(); err != nil {
			t.Error(err)
		}
	}()
	handle := &rootHandle{root: relativeRoot}
	if _, err := handle.ReadFile(filepath.Join(t.TempDir(), "exceptions.json")); err == nil {
		t.Fatal("mixed relative root and absolute path accepted")
	}
	removed := t.TempDir()
	t.Chdir(removed)
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if _, err := openSourceRoot("."); err == nil {
		t.Fatal("missing working directory accepted")
	}
}
