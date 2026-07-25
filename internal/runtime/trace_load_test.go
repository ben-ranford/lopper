package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestLoadTrace(t *testing.T) {
	trace, err := loadTraceFromContent(t, `{"kind":"resolve","module":"`+lodashMapModule+`","resolved":"file:///repo/node_modules/lodash/map.js","parent":"file:///repo/src/index.js","entrypoint":"file:///repo/src/main.js"}`+"\n"+`{"kind":"require","module":"@scope/pkg/lib","resolved":"/repo/node_modules/@scope/pkg/lib/index.js","parent":"/repo/src/index.cjs","entrypoint":"/repo/src/start.cjs"}`+"\n")

	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if trace.DependencyLoads["lodash"] != 1 {
		t.Fatalf("expected lodash load count=1, got %d", trace.DependencyLoads["lodash"])
	}
	if trace.DependencyLoads[scopePkgDependency] != 1 {
		t.Fatalf("expected %s load count=1, got %d", scopePkgDependency, trace.DependencyLoads[scopePkgDependency])
	}
	if got := trace.DependencyModules["lodash"][lodashMapModule]; got != 1 {
		t.Fatalf("expected lodash module count 1, got %d", got)
	}
	if got := trace.DependencyParents["lodash"]["/repo/src/index.js"]; got != 1 {
		t.Fatalf("expected lodash parent count 1, got %d", got)
	}
	if got := trace.DependencyEntrypoints["lodash"]["/repo/src/main.js"]; got != 1 {
		t.Fatalf("expected lodash entrypoint count 1, got %d", got)
	}
	if got := trace.DependencySymbols["lodash"][lodashMapModule+"\x00map"]; got != 1 {
		t.Fatalf("expected lodash symbol count 1, got %d", got)
	}
}

func TestLoadTracePythonLanguageEvents(t *testing.T) {
	trace, err := loadTraceFromContent(t, `{"language":"python","module":"requests.sessions","parent":"/repo/app.py","entrypoint":"/repo/app.py"}`+"\n"+`{"language":"python","dependency":"python-dateutil","module":"dateutil.parser","resolved":"/repo/.venv/lib/python3.12/site-packages/dateutil/parser.py"}`+"\n")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}

	requestsKey := DependencyKey{Language: runtimeLanguagePython, Name: "requests"}
	if got := trace.DependencyLoadsByLanguage[requestsKey]; got != 1 {
		t.Fatalf("expected python requests load count=1, got %d", got)
	}
	if got := trace.DependencyLoads["requests"]; got != 0 {
		t.Fatalf("did not expect python loads in legacy JS counters, got %d", got)
	}
	if got := trace.DependencyModulesByLanguage[requestsKey]["requests.sessions"]; got != 1 {
		t.Fatalf("expected python module count 1, got %d", got)
	}
	if got := trace.DependencyParentsByLanguage[requestsKey]["/repo/app.py"]; got != 1 {
		t.Fatalf("expected python parent count 1, got %d", got)
	}
	if got := trace.DependencySymbolsByLanguage[requestsKey]["requests.sessions\x00sessions"]; got != 1 {
		t.Fatalf("expected python symbol count 1, got %d", got)
	}

	dateutilKey := DependencyKey{Language: runtimeLanguagePython, Name: "python-dateutil"}
	if got := trace.DependencyLoadsByLanguage[dateutilKey]; got != 1 {
		t.Fatalf("expected python-dateutil load count=1, got %d", got)
	}
}

func TestLoadTracePythonLanguageEventsCanonicalizePyPIKeys(t *testing.T) {
	trace, err := loadTraceFromContent(t, `{"language":"python","dependency":"My__Package","module":"My__Package.client"}`+"\n"+`{"language":"python","dependency":"my_.package","module":"my_.package.api"}`+"\n")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}

	key := DependencyKey{Language: runtimeLanguagePython, Name: "my-package"}
	if got := trace.DependencyLoadsByLanguage[key]; got != 2 {
		t.Fatalf("expected canonical my-package load count=2, got %d from %#v", got, trace.DependencyLoadsByLanguage)
	}
	if len(trace.DependencyLoadsByLanguage) != 1 {
		t.Fatalf("expected one canonical Python dependency key, got %#v", trace.DependencyLoadsByLanguage)
	}
}

func TestLoadTraceInvalidLine(t *testing.T) {
	if _, err := loadTraceFromContent(t, "{not-json}\n"); err == nil {
		t.Fatalf("expected parse error for invalid NDJSON")
	}
}

func TestLoadTraceScannerErrTooLong(t *testing.T) {
	tooLong := strings.Repeat("x", 80*1024)
	_, err := loadTraceFromContent(t, tooLong)
	if err == nil {
		t.Fatalf("expected scanner error for oversized line")
	}
}

func TestLoadTraceParseErrorIncludesLineNumber(t *testing.T) {
	_, err := loadTraceFromContent(t, "{\"module\":\"ok\"}\n{not-json}\n")
	if err == nil {
		t.Fatalf("expected parse error")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("expected line number in parse error, got %v", err)
	}
}

func TestLoadTraceOversizedFile(t *testing.T) {
	_, err := loadTraceFromContent(t, oversizedRuntimeTraceContent())
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized trace to fail with ErrFileTooLarge, got %v", err)
	}
}

func TestLoadTraceContextCanceledBeforeOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := LoadContext(ctx, filepath.Join(t.TempDir(), "missing.ndjson"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation before file open, got %v", err)
	}
}

func TestLoadTraceSkipsBlankLines(t *testing.T) {
	trace, err := loadTraceFromContent(t, "\n   \n{\"module\":\""+lodashMapModule+"\"}\n")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyLoads["lodash"]; got != 1 {
		t.Fatalf("expected lodash load count 1, got %d", got)
	}
}

func TestLoadTraceMissingFileError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.ndjson"))
	if err == nil {
		t.Fatalf("expected missing-file error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestLoadTraceSkipsEventsWithoutDependencies(t *testing.T) {
	trace, err := loadTraceFromContent(t, "{\"module\":\"./local\"}\n{\"resolved\":\"/repo/src/index.js\"}\n")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if len(trace.DependencyLoads) != 0 || len(trace.DependencyModules) != 0 || len(trace.DependencySymbols) != 0 {
		t.Fatalf("expected dependency-free events to be ignored, got %#v", trace)
	}
}

func TestLoadTraceReadsFinalEventWithoutTrailingNewline(t *testing.T) {
	trace, err := loadTraceFromContent(t, "{\"module\":\""+lodashMapModule+"\"}")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyLoads["lodash"]; got != 1 {
		t.Fatalf("expected final event without trailing newline to be counted, got %d", got)
	}
}

func TestLoadTraceSkipsFinalBlankLineWithoutTrailingNewline(t *testing.T) {
	trace, err := loadTraceFromContent(t, "{\"module\":\""+lodashMapModule+"\"}\n   ")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyLoads["lodash"]; got != 1 {
		t.Fatalf("expected blank final line to be ignored, got %d", got)
	}
}

func TestLoadTraceRejectsSymlinkPath(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "target.ndjson")
	if err := os.WriteFile(targetPath, []byte("{\"module\":\""+lodashMapModule+"\"}\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	linkPath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.Symlink(filepath.Base(targetPath), linkPath); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	_, err := Load(linkPath)
	if err == nil || !strings.Contains(err.Error(), "runtime trace path is a symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestLoadTraceRejectsDirectoryPath(t *testing.T) {
	traceDir := filepath.Join(t.TempDir(), "trace-dir")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace dir: %v", err)
	}

	_, err := Load(traceDir)
	if err == nil || !strings.Contains(err.Error(), "runtime trace path is not a regular file") {
		t.Fatalf("expected directory rejection, got %v", err)
	}
}

func TestLoadTraceLineCountLimit(t *testing.T) {
	content := strings.Repeat("\n", maxRuntimeTraceLines) + "{\"module\":\"" + lodashMapModule + "\"}\n"
	_, err := loadTraceFromContent(t, content)
	if err == nil || !strings.Contains(err.Error(), "maximum line count") {
		t.Fatalf("expected line count limit error, got %v", err)
	}
}

func TestLoadTraceEventCountLimit(t *testing.T) {
	line := "{}\n"
	_, err := loadTraceFromContent(t, strings.Repeat(line, maxRuntimeTraceEvents+1))
	if err == nil || !strings.Contains(err.Error(), "maximum event count") {
		t.Fatalf("expected event count limit error, got %v", err)
	}
}

func TestLoadTraceObjectFieldLimit(t *testing.T) {
	fields := make([]string, 0, maxRuntimeTraceObjectFields+1)
	for i := 0; i <= maxRuntimeTraceObjectFields; i++ {
		fields = append(fields, "\"extra"+strconv.Itoa(i)+"\":\"x\"")
	}
	content := "{" + strings.Join(fields, ",") + "}\n"
	_, err := loadTraceFromContent(t, content)
	if err == nil || !strings.Contains(err.Error(), "maximum object entries") {
		t.Fatalf("expected object field limit error, got %v", err)
	}
}

func TestLoadTraceNameLimits(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "dependency",
			content: "{\"language\":\"python\",\"dependency\":\"" + strings.Repeat("d", maxRuntimeTraceNameBytes+1) + "\",\"module\":\"pkg.mod\"}\n",
			want:    "dependency name exceeds",
		},
		{
			name:    "module",
			content: "{\"language\":\"python\",\"dependency\":\"pkg\",\"module\":\"" + strings.Repeat("m", maxRuntimeTraceNameBytes+1) + "\"}\n",
			want:    "module name exceeds",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTraceFromContent(t, tc.content)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected name limit error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoadTraceContextCanceledDuringParsing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		runtimeTraceEventParseHook = nil
	})
	runtimeTraceEventParseHook = func(field string) {
		if field == "module" {
			cancel()
		}
	}

	_, err := loadTraceFromContentContext(ctx, t, "{\"module\":\""+lodashMapModule+"\",\"entrypoint\":\"/repo/src/main.js\"}\n")
	if err == nil {
		t.Fatal("expected context cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestLoadTraceReturnsCloseErrorAfterSuccessfulRead(t *testing.T) {
	restore := stubLoadRuntimeTraceFile(func(string) (io.ReadCloser, error) {
		return &runtimeTraceReadCloser{
			Reader:   strings.NewReader("{\"module\":\"" + lodashMapModule + "\"}\n"),
			closeErr: errors.New("close failed"),
		}, nil
	})
	t.Cleanup(restore)

	_, err := LoadContext(context.Background(), "ignored.ndjson")
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("expected close error after successful read, got %v", err)
	}
}

func TestLoadTracePropagatesReadError(t *testing.T) {
	restore := stubLoadRuntimeTraceFile(func(string) (io.ReadCloser, error) {
		return &runtimeTraceReadCloser{
			Reader:   &errorReader{err: errors.New("read failed")},
			closeErr: nil,
		}, nil
	})
	t.Cleanup(restore)

	_, err := LoadContext(context.Background(), "ignored.ndjson")
	if err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("expected read error, got %v", err)
	}
}

func TestParseRuntimeTraceEventCanceledContextBeforeDecode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := parseRuntimeTraceEvent(ctx, []byte("{\"module\":\""+lodashMapModule+"\"}"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected parse cancellation before decode, got %v", err)
	}
}

func TestParseRuntimeTraceEventRejectsInvalidFieldTypes(t *testing.T) {
	t.Parallel()

	for _, field := range []string{
		"language",
		"dependency",
		"module",
		"resolved",
		"kind",
		"parent",
		"entrypoint",
	} {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()

			_, err := parseRuntimeTraceEvent(context.Background(), []byte("{\""+field+"\":{}}"))
			if err == nil {
				t.Fatal("expected decode error")
			}
			if !strings.Contains(err.Error(), "decode "+field) {
				t.Fatalf("expected decode error for %s, got %v", field, err)
			}
		})
	}
}

func TestRuntimeTraceByteLimitReaderZeroLengthRead(t *testing.T) {
	reader := newRuntimeTraceByteLimitReader(strings.NewReader("data"), 4)

	n, err := reader.Read(nil)
	if n != 0 || err != nil {
		t.Fatalf("expected zero-length read to return 0, nil, got %d, %v", n, err)
	}
}

func TestRuntimeTraceByteLimitReaderRejectsReadAfterBudgetExhausted(t *testing.T) {
	reader := newRuntimeTraceByteLimitReader(strings.NewReader("x"), 0)
	buf := make([]byte, 4)

	n, err := reader.Read(buf)
	if n != 0 {
		t.Fatalf("expected no bytes after budget exhaustion, got %d", n)
	}
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge after budget exhaustion, got %v", err)
	}
}

func TestRuntimeTraceByteLimitReaderRejectsNegativeRemainingBudget(t *testing.T) {
	reader := &runtimeTraceByteLimitReader{
		reader:    strings.NewReader("x"),
		remaining: -1,
	}
	buf := make([]byte, 4)

	n, err := reader.Read(buf)
	if n != 0 {
		t.Fatalf("expected no bytes when remaining budget is negative, got %d", n)
	}
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge for negative remaining budget, got %v", err)
	}
}

func TestRuntimeTraceByteLimitReaderPreservesUnderlyingEOFWithinBudget(t *testing.T) {
	reader := newRuntimeTraceByteLimitReader(strings.NewReader("xy"), 4)
	buf := make([]byte, 4)

	n, err := reader.Read(buf)
	if n != 2 {
		t.Fatalf("expected to read 2 bytes within budget, got %d", n)
	}
	if err != nil {
		t.Fatalf("expected nil error while bytes remain within budget, got %v", err)
	}

	n, err = reader.Read(buf)
	if n != 0 {
		t.Fatalf("expected EOF read to return 0 bytes, got %d", n)
	}
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected underlying EOF after buffered bytes are consumed, got %v", err)
	}
}

func TestOpenRuntimeTraceFileRejectsFileHandleWithoutStat(t *testing.T) {
	lstat := func(string) (fs.FileInfo, error) {
		return &fakeFileInfo{name: "trace.ndjson", mode: 0o600}, nil
	}
	open := func(string) (io.ReadCloser, error) {
		return &runtimeTraceReadCloser{}, nil
	}

	restore := stubRuntimeTraceFileOpenState(lstat, open, nil)
	t.Cleanup(restore)

	_, err := openRuntimeTraceFile("trace.ndjson")
	if err == nil || !strings.Contains(err.Error(), "does not support stat") {
		t.Fatalf("expected stat support error, got %v", err)
	}
}

func TestOpenRuntimeTraceFileReturnsStatError(t *testing.T) {
	lstat := func(string) (fs.FileInfo, error) {
		return &fakeFileInfo{name: "trace.ndjson", mode: 0o600}, nil
	}
	open := func(string) (io.ReadCloser, error) {
		return &runtimeTraceStatReadCloser{
			Reader:  strings.NewReader(""),
			statErr: errors.New("stat failed"),
		}, nil
	}

	restore := stubRuntimeTraceFileOpenState(lstat, open, nil)
	t.Cleanup(restore)

	_, err := openRuntimeTraceFile("trace.ndjson")
	if err == nil || !strings.Contains(err.Error(), "stat failed") {
		t.Fatalf("expected stat error, got %v", err)
	}
}

func TestOpenRuntimeTraceFileRejectsOpenedNonRegularFile(t *testing.T) {
	lstat := func(string) (fs.FileInfo, error) {
		return &fakeFileInfo{name: "trace.ndjson", mode: 0o600}, nil
	}
	open := func(string) (io.ReadCloser, error) {
		return &runtimeTraceStatReadCloser{
			Reader: strings.NewReader(""),
			info:   &fakeFileInfo{name: "trace.ndjson", mode: os.ModeDir | 0o755},
		}, nil
	}
	sameFile := func(fs.FileInfo, fs.FileInfo) bool { return true }

	restore := stubRuntimeTraceFileOpenState(lstat, open, sameFile)
	t.Cleanup(restore)

	_, err := openRuntimeTraceFile("trace.ndjson")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular file error, got %v", err)
	}
}

func TestOpenRuntimeTraceFileRejectsPathChangeDuringOpen(t *testing.T) {
	lstat := func(string) (fs.FileInfo, error) {
		return &fakeFileInfo{name: "before.ndjson", mode: 0o600}, nil
	}
	open := func(string) (io.ReadCloser, error) {
		return &runtimeTraceStatReadCloser{
			Reader: strings.NewReader(""),
			info:   &fakeFileInfo{name: "after.ndjson", mode: 0o600},
		}, nil
	}
	sameFile := func(fs.FileInfo, fs.FileInfo) bool { return false }

	restore := stubRuntimeTraceFileOpenState(lstat, open, sameFile)
	t.Cleanup(restore)

	_, err := openRuntimeTraceFile("trace.ndjson")
	if err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("expected path change error, got %v", err)
	}
}

func oversizedRuntimeTraceContent() string {
	const maxRuntimeTraceBytesForTest = 8 * 1024 * 1024

	line := "{\"module\":\"" + leftPadModule + "\"}\n"
	repeat := maxRuntimeTraceBytesForTest/len(line) + 1
	return strings.Repeat(line, repeat)
}

func stubLoadRuntimeTraceFile(stub func(string) (io.ReadCloser, error)) func() {
	previous := loadRuntimeTraceFile
	loadRuntimeTraceFile = stub
	return func() {
		loadRuntimeTraceFile = previous
	}
}

func stubRuntimeTraceFileOpenState(lstat func(string) (fs.FileInfo, error), open func(string) (io.ReadCloser, error), sameFile func(fs.FileInfo, fs.FileInfo) bool) func() {
	previousLstat := runtimeTraceLstat
	previousOpen := runtimeTraceOpenFile
	previousSameFile := runtimeTraceSameFile

	runtimeTraceLstat = lstat
	runtimeTraceOpenFile = open
	if sameFile != nil {
		runtimeTraceSameFile = sameFile
	}

	return func() {
		runtimeTraceLstat = previousLstat
		runtimeTraceOpenFile = previousOpen
		runtimeTraceSameFile = previousSameFile
	}
}

type runtimeTraceReadCloser struct {
	io.Reader
	closeErr error
}

func (r *runtimeTraceReadCloser) Close() error {
	return r.closeErr
}

type runtimeTraceStatReadCloser struct {
	io.Reader
	info     fs.FileInfo
	statErr  error
	closeErr error
}

func (r *runtimeTraceStatReadCloser) Close() error {
	return r.closeErr
}

func (r *runtimeTraceStatReadCloser) Stat() (fs.FileInfo, error) {
	if r.statErr != nil {
		return nil, r.statErr
	}
	return r.info, nil
}

type errorReader struct {
	err error
}

func (r *errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type fakeFileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (f *fakeFileInfo) Name() string       { return f.name }
func (f *fakeFileInfo) Size() int64        { return f.size }
func (f *fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f *fakeFileInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (f *fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f *fakeFileInfo) Sys() any           { return fmt.Sprintf("%s:%d", f.name, f.mode) }
