package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const validWhy = "Repository-controlled manifest is still read without a declared byte limit."

func TestListZeroLimitReadsClassifiesParserBoundaries(t *testing.T) {
	root := t.TempDir()
	writeGoFiles(t, root, map[string]string{
		"unbounded.go": `package sample

import "github.com/ben-ranford/lopper/internal/safeio"

func loadManifest(root, path string) {
	_, _ = safeio.ReadFileUnder(root, path)
	_, _ = safeio.ReadFileUnder(root, path)
}

func loadExact(path string) {
	_, _ = safeio.ReadFile(path)
}

func loadOpen(root safeio.Root, path string) {
	_, _ = safeio.ReadFileWithinRoot(root, path)
}

const zeroLimit = 0
const shifted = 1 << 20
const summed = 2 * 1024
const negative = -1
const parenthesized = (0)
const divided = 0 / 1

func loadLimited(root, path string, maxBytes int64) {
	_, _ = safeio.ReadFileUnderLimit(root, path, 0)
	_, _ = safeio.ReadFileUnderLimit(root, path, -1)
	_, _ = safeio.ReadFileUnderLimit(root, path, zeroLimit)
	_, _ = safeio.ReadFileUnderLimit(root, path, negative)
	_, _ = safeio.ReadFileUnderLimit(root, path, parenthesized)
	_, _ = safeio.ReadFileUnderLimit(root, path, divided)
	_, _ = safeio.ReadFileUnderLimit(root, path, shifted)
	_, _ = safeio.ReadFileUnderLimit(root, path, summed)
	_, _ = safeio.ReadFileUnderLimit(root, path, maxBytes)
	_, _ = safeio.ReadFileLimit()
	_ = safeio.ReadFileUnderLimit
}

func (s *box) read(path string) {
	_, _ = safeio.ReadFile(path)
}

type box struct{}
`,
		"alias.go": `package sample

import sio "github.com/ben-ranford/lopper/internal/safeio"

func aliased(path string) {
	_, _ = sio.ReadFile(path)
}
`,
		"dot.go": `package sample

import . "github.com/ben-ranford/lopper/internal/safeio"

func dotted(path string) {
	_, _ = ReadFile(path)
}
`,
		"blank.go": `package sample

import _ "github.com/ben-ranford/lopper/internal/safeio"

func ignored(path string) {
	_ = path
}
`,
		"other.go": `package sample

import safeio "example.com/not-safeio"

func foreign(path string) {
	_, _ = safeio.ReadFile(path)
	_ = "safeio.ReadFile(path)"
	// safeio.ReadFile(path)
}
`,
		"values.go": `package sample

import "github.com/ben-ranford/lopper/internal/safeio"

var readFile = safeio.ReadFile

const (
	iotaZero = iota
	iotaOne
)

func useIota(root, path string) {
	_, _ = safeio.ReadFileUnderLimit(root, path, iotaZero)
	_, _ = safeio.ReadFileUnderLimit(root, path, iotaOne)
}
`,
		"methods.go": `package sample

import "github.com/ben-ranford/lopper/internal/safeio"

type pair[T, U any] struct{}

func (p pair[T, U]) read(path string) {
	_, _ = safeio.ReadFile(path)
}

func (p *pair[T, U]) readUnder(root, path string) {
	_, _ = safeio.ReadFileUnder(root, path)
}

func (p *[]byte) raw(path string) {
	_, _ = safeio.ReadFile(path)
}
`,
		"same/read.go": `package safeio

func ReadFileUnder(root, path string) ([]byte, error) {
	return ReadFileUnderLimit(root, path, 0)
}

func ReadFileUnderLimit(root, path string, max int64) ([]byte, error) {
	return ReadFileLimit(path, max)
}

func ReadFileLimit(path string, max int64) ([]byte, error) {
	return nil, nil
}

func positive(path string) {
	_, _ = ReadFileLimit(path, 4)
}
`,
		"nested/skip_test.go": `package nested

import "github.com/ben-ranford/lopper/internal/safeio"

func TestRead(t *testing.T) {
	_, _ = safeio.ReadFile("x")
}
`,
		"vendor/hidden/read.go": `package hidden

import "github.com/ben-ranford/lopper/internal/safeio"

func hidden(path string) {
	_, _ = safeio.ReadFile(path)
}
`,
		".artifacts/generated.go": `package generated

import "github.com/ben-ranford/lopper/internal/safeio"

func generated(path string) {
	_, _ = safeio.ReadFile(path)
}
`,
	})

	refs, err := listZeroLimitReads(root)
	if err != nil {
		t.Fatalf("list reads: %v", err)
	}
	got := map[string]int{}
	for _, ref := range refs {
		got[formatReference(ref)]++
	}
	want := []string{
		"unbounded.go\tloadManifest\tsafeio.ReadFileUnder\t1",
		"unbounded.go\tloadManifest\tsafeio.ReadFileUnder\t2",
		"unbounded.go\tloadExact\tsafeio.ReadFile\t1",
		"unbounded.go\tloadOpen\tsafeio.ReadFileWithinRoot\t1",
		"unbounded.go\tloadLimited\tsafeio.ReadFileUnderLimit\t1",
		"unbounded.go\tloadLimited\tsafeio.ReadFileUnderLimit\t2",
		"unbounded.go\tloadLimited\tsafeio.ReadFileUnderLimit\t3",
		"unbounded.go\tloadLimited\tsafeio.ReadFileUnderLimit\t4",
		"unbounded.go\tloadLimited\tsafeio.ReadFileUnderLimit\t5",
		"unbounded.go\tloadLimited\tsafeio.ReadFileUnderLimit\t6",
		"unbounded.go\tloadLimited\tsafeio.ReadFileLimit\t1",
		"unbounded.go\tbox.read\tsafeio.ReadFile\t1",
		"alias.go\taliased\tsafeio.ReadFile\t1",
		"dot.go\tdotted\tsafeio.ReadFile\t1",
		"values.go\tvar:readFile\tsafeio.ReadFile\t1",
		"values.go\tuseIota\tsafeio.ReadFileUnderLimit\t1",
		"methods.go\tpair.read\tsafeio.ReadFile\t1",
		"methods.go\tpair.readUnder\tsafeio.ReadFileUnder\t1",
		"methods.go\treceiver.raw\tsafeio.ReadFile\t1",
		"same/read.go\tReadFileUnder\tsafeio.ReadFileUnderLimit\t1",
	}
	if len(got) != len(want) {
		t.Fatalf("reads = %d, want %d\n%s", len(got), len(want), strings.Join(keys(got), "\n"))
	}
	for _, key := range want {
		if got[key] != 1 {
			t.Fatalf("missing %s in\n%s", key, strings.Join(keys(got), "\n"))
		}
	}
}

func TestListZeroLimitReadsRejectsUnusableTrees(t *testing.T) {
	if _, err := listZeroLimitReads(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing root to fail")
	}
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listZeroLimitReads(fileRoot); err == nil {
		t.Fatal("expected file root to fail")
	}

	root := t.TempDir()
	broken := filepath.Join(root, "broken.go")
	if err := os.WriteFile(broken, []byte("package sample\nfunc {"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listZeroLimitReads(root); err == nil || !strings.Contains(err.Error(), "parse ") {
		t.Fatalf("expected parse failure, got %v", err)
	}

	huge := t.TempDir()
	hugeFile, err := os.Create(filepath.Join(huge, "huge.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := hugeFile.Truncate(maxScanFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := hugeFile.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := listZeroLimitReads(huge); err == nil || !strings.Contains(err.Error(), "scan limit") {
		t.Fatalf("expected scan limit failure, got %v", err)
	}
}

func TestListZeroLimitReadsRejectsSpecialFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fifo files are a Unix input")
	}
	root := t.TempDir()
	if err := mkfifo(filepath.Join(root, "pipe.go")); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	if _, err := listZeroLimitReads(root); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular Go file to fail, got %v", err)
	}
}

func TestCheckAllowlistRequiresMatchingRationale(t *testing.T) {
	refs := []Reference{{
		Path:     "internal/sample/read.go",
		Function: "load",
		Callee:   "safeio.ReadFileUnder",
		Index:    1,
	}}
	path := filepath.Join(t.TempDir(), "allow.txt")
	body := "internal/sample/read.go\tload\tsafeio.ReadFileUnder\t1\t" + validWhy + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err := checkAllowlist(path, refs)
	if err != nil || len(findings) != 0 {
		t.Fatalf("expected allowlist match, findings=%v err=%v", findings, err)
	}

	findings, err = checkAllowlist(path, nil)
	if err != nil || len(findings) != 1 || !strings.Contains(findings[0], "stale") {
		t.Fatalf("expected stale entry, findings=%v err=%v", findings, err)
	}

	findings, err = checkAllowlist(path, append(refs, refs[0]))
	if err != nil || len(findings) != 1 || !strings.Contains(findings[0], "duplicates") {
		t.Fatalf("expected duplicate scan, findings=%v err=%v", findings, err)
	}

	missing := filepath.Join(t.TempDir(), "missing.txt")
	if _, err := checkAllowlist(missing, refs); err == nil {
		t.Fatal("expected missing allowlist to fail")
	}
	if _, err := checkAllowlist(t.TempDir(), refs); err == nil {
		t.Fatal("expected directory allowlist to fail")
	}
}

func TestLoadAllowlistRejectsMalformedLines(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "columns", line: "only-one", want: "separated by tabs"},
		{name: "path", line: "../x.go\tload\tsafeio.ReadFile\t1\t" + validWhy, want: "repository-relative"},
		{name: "function", line: "x.go\t \tsafeio.ReadFile\t1\t" + validWhy, want: "function"},
		{name: "callee", line: "x.go\tload\tos.ReadFile\t1\t" + validWhy, want: "not a safeio read"},
		{name: "index", line: "x.go\tload\tsafeio.ReadFile\t0\t" + validWhy, want: "positive integer"},
		{name: "short", line: "x.go\tload\tsafeio.ReadFile\t1\ttoo short", want: "at least"},
		{name: "todo", line: "x.go\tload\tsafeio.ReadFile\t1\tTODO explain this zero-limit read later in words", want: "why this read"},
		{name: "control", line: "x.go\tload\tsafeio.ReadFile\t1\t" + validWhy + "\x01", want: "control character"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "allow.txt")
			if err := os.WriteFile(path, []byte(tc.line+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadAllowlist(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "dup.txt")
	line := "x.go\tload\tsafeio.ReadFile\t1\t" + validWhy
	same := "# comment\n\n" + line + "\n" + line + "\n"
	if err := os.WriteFile(path, []byte(same), 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err := checkAllowlist(path, []Reference{{Path: "x.go", Function: "load", Callee: "safeio.ReadFile", Index: 1}})
	if err != nil || len(findings) != 1 || !strings.Contains(findings[0], "duplicate allowlist") {
		t.Fatalf("expected duplicate entry, findings=%v err=%v", findings, err)
	}
	repeated := line + "\nx.go\tother\tsafeio.ReadFileUnder\t1\t" + validWhy + "\n"
	if err := os.WriteFile(path, []byte(repeated), 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err = checkAllowlist(path, []Reference{
		{Path: "x.go", Function: "load", Callee: "safeio.ReadFile", Index: 1},
		{Path: "x.go", Function: "other", Callee: "safeio.ReadFileUnder", Index: 1},
	})
	if err != nil || len(findings) != 1 || !strings.Contains(findings[0], "rationale duplicates") {
		t.Fatalf("expected duplicate rationale, findings=%v err=%v", findings, err)
	}

	huge := filepath.Join(t.TempDir(), "huge.txt")
	handle, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Truncate(maxScanFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAllowlist(huge); err == nil || !strings.Contains(err.Error(), "allowlist limit") {
		t.Fatalf("expected allowlist size failure, got %v", err)
	}
}

func TestEvalIntCoversArithmeticEdges(t *testing.T) {
	known := map[string]int64{"n": 3}
	cases := []struct {
		expr string
		want int64
		ok   bool
	}{
		{expr: "1 + 2", want: 3, ok: true},
		{expr: "n - 1", want: 2, ok: true},
		{expr: "8 >> 1", want: 4, ok: true},
		{expr: "1 << 63", ok: false},
		{expr: "1 >> -1", ok: false},
		{expr: "1 / 0", ok: false},
		{expr: "1 & 1", ok: false},
		{expr: "1.5", ok: false},
		{expr: "missing", ok: false},
		{expr: "int32(4)", want: 4, ok: true},
		{expr: "notInt(4)", ok: false},
		{expr: "byte", ok: false},
		{expr: "9223372036854775808", ok: false},
		{expr: "+1", ok: false},
		{expr: "-missing", ok: false},
		{expr: "1 + missing", ok: false},
		{expr: "(int64)(1)", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, ok := evalSource(t, tc.expr, known)
			if ok != tc.ok || (ok && got != tc.want) {
				t.Fatalf("eval %s = %d, %v; want %d, %v", tc.expr, got, ok, tc.want, tc.ok)
			}
		})
	}
	if _, ok := evalInt(nil, known, 0); ok {
		t.Fatal("expected nil expression to be unknown")
	}
	if err := validateRationale(string([]byte{0xff, 0xfe})); err == nil {
		t.Fatal("expected invalid rationale text to fail")
	}
	if !filepathIsUnsafe(`dir\file.go`) || !filepathIsUnsafe("/abs.go") || !filepathIsUnsafe("dir//file.go") || filepathIsUnsafe("dir/file.go") {
		t.Fatal("unexpected path safety classification")
	}
	if validCallee("safeio.") || validCallee("safeio.ReadFile.Extra") || !validCallee("safeio.ReadFileLimit") {
		t.Fatal("unexpected callee classification")
	}
}

func TestRunReportsAllowlistStatus(t *testing.T) {
	root := t.TempDir()
	writeGoFiles(t, root, map[string]string{
		"read.go": `package sample

import "github.com/ben-ranford/lopper/internal/safeio"

func load(path string) {
	_, _ = safeio.ReadFile(path)
}
`,
	})
	allow := filepath.Join(t.TempDir(), "allow.txt")
	if err := os.WriteFile(allow, []byte("read.go\tload\tsafeio.ReadFile\t1\t"+validWhy+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	if code := run([]string{"-root", root, "-allowlist", allow}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "1 allowlisted") {
		t.Fatalf("success code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-list", "-root", root}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "read.go\tload\tsafeio.ReadFile\t1") {
		t.Fatalf("list code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := os.WriteFile(allow, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-root", root, "-allowlist", allow}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "unapproved") {
		t.Fatalf("unapproved code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	if code := run([]string{"-root", root, "extra"}, &ioDiscard{}, &stderr); code != 2 {
		t.Fatalf("unexpected args code=%d", code)
	}
	if code := run([]string{"-unknown"}, &ioDiscard{}, &stderr); code != 2 {
		t.Fatalf("unknown flag code=%d", code)
	}
	if code := run([]string{"-root", filepath.Join(root, "missing")}, &ioDiscard{}, &stderr); code != 1 {
		t.Fatalf("scan error code=%d", code)
	}
}

type ioDiscard struct{}

func (id *ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

type failWriter struct{}

func (fw *failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestRunWriteFailures(t *testing.T) {
	root := t.TempDir()
	writeGoFiles(t, root, map[string]string{
		"read.go": `package sample

import "github.com/ben-ranford/lopper/internal/safeio"

func load(path string) {
	_, _ = safeio.ReadFile(path)
}
`,
	})
	allow := filepath.Join(root, "allow.txt")
	if err := os.WriteFile(allow, []byte("read.go\tload\tsafeio.ReadFile\t1\t"+validWhy+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-root", root, "extra"}, &ioDiscard{}, &failWriter{}); code != 1 {
		t.Fatalf("argument write failure code=%d", code)
	}
	if code := run([]string{"-root", filepath.Join(root, "missing")}, &ioDiscard{}, &failWriter{}); code != 1 {
		t.Fatalf("scan write failure code=%d", code)
	}
	if code := run([]string{"-list", "-root", root}, &failWriter{}, &ioDiscard{}); code != 1 {
		t.Fatalf("list write failure code=%d", code)
	}
	if code := run([]string{"-root", root, "-allowlist", filepath.Join(root, "missing.txt")}, &ioDiscard{}, &failWriter{}); code != 1 {
		t.Fatalf("allowlist write failure code=%d", code)
	}
	if code := run([]string{"-root", root, "-allowlist", allow}, &failWriter{}, &ioDiscard{}); code != 1 {
		t.Fatalf("summary write failure code=%d", code)
	}
	if err := os.WriteFile(allow, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-root", root, "-allowlist", allow}, &ioDiscard{}, &failWriter{}); code != 1 {
		t.Fatalf("finding write failure code=%d", code)
	}
}

func evalSource(t *testing.T, expr string, known map[string]int64) (int64, bool) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "const.go", "package sample\nconst value = "+expr+"\n", 0)
	if err != nil {
		t.Fatalf("parse %s: %v", expr, err)
	}
	gen, ok := file.Decls[0].(*ast.GenDecl)
	if !ok || len(gen.Specs) != 1 {
		t.Fatalf("parse %s: missing const", expr)
	}
	value, ok := gen.Specs[0].(*ast.ValueSpec)
	if !ok || len(value.Values) != 1 {
		t.Fatalf("parse %s: missing value", expr)
	}
	return evalInt(value.Values[0], known, 0)
}

func TestRepositoryZeroLimitReadsMatchAllowlist(t *testing.T) {
	root := moduleRoot(t)
	var stdout, stderr strings.Builder
	code := run([]string{
		"-root", root,
		"-allowlist", filepath.Join(root, ".github", "safeio-zero-limit-allowlist.txt"),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("repository allowlist check failed:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "allowlisted") {
		t.Fatalf("expected allowlist summary, got %q", stdout.String())
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller unavailable")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestCheckerCoversDefensiveBranches(t *testing.T) {
	if os.Getenv("SAFEIO_READ_CHECK_MAIN") == "1" {
		os.Args = []string{"safeioreadcheck", "-list", "-root", t.TempDir()}
		main()
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckerCoversDefensiveBranches$", "-test.count=1")
	cmd.Env = append(os.Environ(), "SAFEIO_READ_CHECK_MAIN=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("main entrypoint: %v\n%s", err, out)
	}

	root := t.TempDir()
	var stdout, stderr strings.Builder
	if code := run([]string{"-root", root, "-allowlist", filepath.Join(root, "missing.txt")}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "allowlist") {
		t.Fatalf("allowlist error code=%d stderr=%q", code, stderr.String())
	}
	if _, err := parsePackageFiles(root, []string{root}); err == nil {
		t.Fatal("expected reading a directory to fail")
	}
	if _, err := parseAllowlistLine(string([]byte{0xff}), 1); err == nil {
		t.Fatal("expected invalid allowlist text to fail")
	}
	if err := validateRationale(strings.Repeat("a", minRationaleRunes)); err == nil {
		t.Fatal("expected a rationale without spaces to fail")
	}
	if err := validateRationale("tbd " + validWhy); err == nil || !strings.Contains(err.Error(), "why this read") {
		t.Fatalf("tbd rationale error = %v", err)
	}
	if err := validateRationale("fixme " + validWhy); err == nil {
		t.Fatal("expected fixme rationale to fail")
	}

	writeGoFiles(t, root, map[string]string{
		"edges.go": `package sample

import "github.com/ben-ranford/lopper/internal/safeio"

func declared()

type generic[T any] struct{ inner struct{ path string } }

func (g generic[T]) read(path string) {
	_, _ = safeio.ReadFile(path)
	_ = g.inner.path
}
`,
	})
	refs, err := listZeroLimitReads(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Function != "generic.read" {
		t.Fatalf("edge reads = %#v", refs)
	}

	if got := packageValueName(token.TYPE, &ast.ValueSpec{}); got != "value" {
		t.Fatalf("empty value name = %q", got)
	}
	_ = collectConstValues([]*ast.File{
		nil,
		{
			Decls: []ast.Decl{
				&ast.FuncDecl{},
				&ast.GenDecl{Tok: token.CONST, Specs: []ast.Spec{
					&ast.ImportSpec{},
					&ast.ValueSpec{Names: []*ast.Ident{nil, {Name: "extra"}}, Values: []ast.Expr{
						&ast.BasicLit{Kind: token.INT, Value: "1"},
					}},
				}},
			},
		},
	})
}

func writeGoFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func keys(values map[string]int) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	return out
}
