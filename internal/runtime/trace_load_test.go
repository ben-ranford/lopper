package runtime

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestLoadTrace(t *testing.T) {
	repo := t.TempDir()
	for _, path := range []string{
		filepath.Join(repo, "src", "index.js"),
		filepath.Join(repo, "src", "main.js"),
		filepath.Join(repo, "src", "index.cjs"),
		filepath.Join(repo, "src", "start.cjs"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir trace fixture parent: %v", err)
		}
		if err := os.WriteFile(path, []byte("module.exports = 1\n"), 0o600); err != nil {
			t.Fatalf("write trace fixture %q: %v", path, err)
		}
	}

	traceContent := `{"kind":"resolve","module":"` + lodashMapModule + `","resolved":"file:///repo/node_modules/lodash/map.js","parent":"` + fileURLForRuntimeTest(filepath.Join(repo, "src", "index.js"), "") + `","entrypoint":"` + fileURLForRuntimeTest(filepath.Join(repo, "src", "main.js"), "") + `"}` + "\n" +
		`{"kind":"require","module":"@scope/pkg/lib","resolved":"/repo/node_modules/@scope/pkg/lib/index.js","parent":"` + filepath.Join(repo, "src", "index.cjs") + `","entrypoint":"` + filepath.Join(repo, "src", "start.cjs") + `"}` + "\n"
	trace, err := loadTraceFromContentInRepo(t, repo, traceContent)

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
	if got := trace.DependencyParents["lodash"]["src/index.js"]; got != 1 {
		t.Fatalf("expected lodash parent count 1, got %d", got)
	}
	if got := trace.DependencyEntrypoints["lodash"]["src/main.js"]; got != 1 {
		t.Fatalf("expected lodash entrypoint count 1, got %d", got)
	}
	if got := trace.DependencySymbols["lodash"][lodashMapModule+"\x00map"]; got != 1 {
		t.Fatalf("expected lodash symbol count 1, got %d", got)
	}
}

func TestLoadTracePythonLanguageEvents(t *testing.T) {
	repo := t.TempDir()
	appPath := filepath.Join(repo, "app.py")
	if err := os.WriteFile(appPath, []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatalf("write python app fixture: %v", err)
	}

	traceContent := `{"language":"python","module":"requests.sessions","parent":"` + appPath + `","entrypoint":"` + appPath + `"}` + "\n" +
		`{"language":"python","dependency":"python-dateutil","module":"dateutil.parser","resolved":"/repo/.venv/lib/python3.12/site-packages/dateutil/parser.py"}` + "\n"
	trace, err := loadTraceFromContentInRepo(t, repo, traceContent)
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
	if got := trace.DependencyParentsByLanguage[requestsKey]["app.py"]; got != 1 {
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

func TestRuntimeTraceByteLimitReaderReturnsZeroForEmptyDestination(t *testing.T) {
	reader := &runtimeTraceByteLimitReader{reader: strings.NewReader("trace"), remaining: 4}

	n, err := reader.Read(nil)
	if n != 0 || err != nil {
		t.Fatalf("expected empty destination read to return (0, nil), got (%d, %v)", n, err)
	}
}

func TestRuntimeTraceByteLimitReaderRejectsReadsAfterBudgetExhaustion(t *testing.T) {
	reader := &runtimeTraceByteLimitReader{reader: strings.NewReader("trace"), remaining: 0}

	buf := make([]byte, 8)
	n, err := reader.Read(buf)
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected exhausted reader to fail with ErrFileTooLarge, got %v", err)
	}
	if n != 0 {
		t.Fatalf("expected exhausted reader to return zero bytes, got %d", n)
	}
	if reader.remaining != 0 {
		t.Fatalf("expected exhausted reader budget to remain zero, got %d", reader.remaining)
	}
}

func TestRuntimeTraceByteLimitReaderClampsNegativeRemainingBudget(t *testing.T) {
	reader := &runtimeTraceByteLimitReader{reader: strings.NewReader("x"), remaining: -1}

	buf := make([]byte, 1)
	n, err := reader.Read(buf)
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected negative remaining budget to fail with ErrFileTooLarge, got %v", err)
	}
	if n != 0 {
		t.Fatalf("expected negative remaining budget to return zero bytes, got %d", n)
	}
	if reader.remaining != 0 {
		t.Fatalf("expected negative remaining budget to clamp to zero, got %d", reader.remaining)
	}
}

func TestRuntimeTraceByteLimitReaderReturnsUnderlyingReadWhenWithinBudget(t *testing.T) {
	reader := &runtimeTraceByteLimitReader{reader: strings.NewReader("ok"), remaining: 4}

	buf := make([]byte, 4)
	n, err := reader.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("expected within-budget read to preserve underlying result, got %v", err)
	}
	if got := string(buf[:n]); got != "ok" {
		t.Fatalf("expected within-budget read to return payload, got %q", got)
	}
	if reader.remaining != 2 {
		t.Fatalf("expected remaining budget 2 after read, got %d", reader.remaining)
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

func TestLoadTraceRedactsContextOutsideRepoBoundary(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "main.js"), []byte("console.log('x')\n"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}
	if err := os.WriteFile(outside, []byte("console.log('y')\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	trace, err := loadTraceFromContentInRepo(t, repo, `{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"`+outside+`","entrypoint":"file://`+filepath.ToSlash(filepath.Join(repo, "src", "main.js"))+`"}`+"\n")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyParents["lodash"]; len(got) != 0 {
		t.Fatalf("expected outside parent to be redacted, got %#v", got)
	}
	if trace.DependencyEntrypoints["lodash"]["src/main.js"] != 1 {
		t.Fatalf("expected repo-relative entrypoint, got %#v", trace.DependencyEntrypoints["lodash"])
	}
}

func TestLoadTraceRedactsWindowsAbsoluteContextOnAnyHost(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "main.js"), []byte("console.log('x')\n"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}

	content :=
		`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"C:\\Users\\alice\\project\\main.js","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"\\\\server\\share\\project\\main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"//server/share/project/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"\\/server/share/project/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"/\\server/share/project/main.js"}` + "\n"
	trace, err := loadTraceFromContentInRepo(t, repo, content)
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyParents["lodash"]; got["src/main.js"] != 4 || len(got) != 1 {
		t.Fatalf("expected only repo-relative parent to remain, got %#v", got)
	}
	if got := trace.DependencyEntrypoints["lodash"]; got["src/main.js"] != 1 || len(got) != 1 {
		t.Fatalf("expected only repo-relative entrypoint to remain, got %#v", got)
	}
}

func TestLoadTraceParsesAndConfinesFileURLContext(t *testing.T) {
	repo := t.TempDir()
	mainPath := filepath.Join(repo, "src", "main.js")
	spacePath := filepath.Join(repo, "src", "hello world.js")
	if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	for _, path := range []string{mainPath, spacePath} {
		if err := os.WriteFile(path, []byte("console.log('x')\n"), 0o600); err != nil {
			t.Fatalf("write repo file: %v", err)
		}
	}
	outsidePath := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(outsidePath, []byte("console.log('y')\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	mainURL := fileURLForRuntimeTest(mainPath, "")
	localhostSpaceURL := fileURLForRuntimeTest(spacePath, "LOCALHOST")
	repoURL := fileURLForRuntimeTest(repo, "")
	content :=
		`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"` + mainURL + `","entrypoint":"` + localhostSpaceURL + `"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"file://localhost/C:/Users/alice/project/main.js","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"file://server/share/project/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"` + strings.TrimSuffix(repoURL, "/") + `/src/bad%ZZ.js","entrypoint":"file://localhost/%43%3A%2FUsers/alice/project/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"` + strings.TrimSuffix(repoURL, "/") + `/src%2F..%2F..%2FUsers/alice/main.js","entrypoint":"file://localhost/%2F%2Fserver/share/project/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"x:private-token","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"a:foo/bar.js","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"C:Users/alice/private.js","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"data:text/plain,secret","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"mailto:test@example.com","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"https:foo","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"https:/foo","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"node:internal/modules/cjs/loader","entrypoint":"lodash/map"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"` + fileURLForRuntimeTest(outsidePath, "") + `","entrypoint":"` + mainURL + `"}` + "\n"
	trace, err := loadTraceFromContentInRepo(t, repo, content)
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyParents["lodash"]; got["src/main.js"] != 2 || got["node:internal/modules/cjs/loader"] != 1 || len(got) != 2 {
		t.Fatalf("expected only local parent file URLs to remain, got %#v", got)
	}
	if got := trace.DependencyEntrypoints["lodash"]; got["src/main.js"] != 9 || got["src/hello world.js"] != 1 || got["lodash/map"] != 1 || len(got) != 3 {
		t.Fatalf("expected only local entrypoint file URLs to remain decoded, got %#v", got)
	}
}

func TestLoadTraceRejectsSymlinkEscapes(t *testing.T) {
	repo := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.js"), []byte("module.exports = 1\n"), 0o600); err != nil {
		t.Fatalf("write outside secret: %v", err)
	}
	linkPath := filepath.Join(repo, "src", "linked.js")
	if err := os.Symlink(filepath.Join(outsideDir, "secret.js"), linkPath); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}

	trace, err := loadTraceFromContentInRepo(t, repo, `{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"`+linkPath+`","entrypoint":"`+linkPath+`"}`+"\n")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyParents["lodash"]; len(got) != 0 {
		t.Fatalf("expected symlink-escaped parent to be redacted, got %#v", got)
	}
	if got := trace.DependencyEntrypoints["lodash"]; len(got) != 0 {
		t.Fatalf("expected symlink-escaped entrypoint to be redacted, got %#v", got)
	}
}

func TestLoadTraceCanonicalizesRepoRootOncePerLoad(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "main.js"), []byte("console.log('x')\n"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}

	calls := 0
	resolveRepoRoot := func(repoPath string) string {
		calls++
		return resolvedRuntimeRepoRoot(repoPath)
	}

	content :=
		`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n"
	tracePath := testutil.WriteTempFile(t, filepath.Join("runtime", "trace.ndjson"), content)
	trace, err := load(tracePath, traceLoadOptions{repoRoot: repo, resolveRepoRoot: resolveRepoRoot})
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if calls != 1 {
		t.Fatalf("expected repo root to resolve once per load, got %d", calls)
	}
	if trace.DependencyParents["lodash"]["src/main.js"] != 2 {
		t.Fatalf("expected both parent samples to be counted, got %#v", trace.DependencyParents["lodash"])
	}
	if trace.DependencyEntrypoints["lodash"]["src/main.js"] != 2 {
		t.Fatalf("expected both entrypoint samples to be counted, got %#v", trace.DependencyEntrypoints["lodash"])
	}
}

func TestLoadTraceRedactsEmbeddedRelativeTraversal(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "main.js"), []byte("console.log('x')\n"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}

	trace, err := loadTraceFromContentInRepo(t, repo, `{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/../../Users/name/file.js","entrypoint":"src/main.js"}`+"\n")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyParents["lodash"]; len(got) != 0 {
		t.Fatalf("expected embedded traversal parent to be redacted, got %#v", got)
	}
	if trace.DependencyEntrypoints["lodash"]["src/main.js"] != 1 {
		t.Fatalf("expected normal repo-relative entrypoint to be preserved, got %#v", trace.DependencyEntrypoints["lodash"])
	}
}

func TestLoadTracePreservesPackageStyleLabels(t *testing.T) {
	content :=
		`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"node:internal/modules/cjs/loader","entrypoint":"lodash/map"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"lodash/fp.js","entrypoint":"@scope/pkg/index.js"}` + "\n"
	trace, err := loadTraceFromContentInRepo(t, t.TempDir(), content)
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if trace.DependencyParents["lodash"]["node:internal/modules/cjs/loader"] != 1 {
		t.Fatalf("expected package-style parent label to be preserved, got %#v", trace.DependencyParents["lodash"])
	}
	if trace.DependencyEntrypoints["lodash"]["lodash/map"] != 1 {
		t.Fatalf("expected package-style entrypoint label to be preserved, got %#v", trace.DependencyEntrypoints["lodash"])
	}
	if trace.DependencyParents["lodash"]["lodash/fp.js"] != 1 {
		t.Fatalf("expected file-like package parent label to be preserved, got %#v", trace.DependencyParents["lodash"])
	}
	if trace.DependencyEntrypoints["lodash"]["@scope/pkg/index.js"] != 1 {
		t.Fatalf("expected scoped file-like package entrypoint label to be preserved, got %#v", trace.DependencyEntrypoints["lodash"])
	}
}

func TestLoadTracePreservesModuleOnlyPackageEventsWhenResolvedPathIsRedacted(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "main.js"), []byte("console.log('x')\n"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}

	trace, err := loadTraceFromContentInRepo(t, repo, `{"module":"fixture-dep","resolved":"","parent":"src/main.js","entrypoint":"src/main.js"}`+"\n")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	assertModuleOnlyPackageEvent(t, trace, "fixture-dep", "src/main.js")
}

func assertModuleOnlyPackageEvent(t *testing.T, trace Trace, dep, context string) {
	t.Helper()
	if trace.DependencyLoads[dep] != 1 {
		t.Fatalf("expected module-only dependency load to survive, got %d", trace.DependencyLoads[dep])
	}
	if trace.DependencyModules[dep][dep] != 1 {
		t.Fatalf("expected module-only dependency module attribution, got %#v", trace.DependencyModules[dep])
	}
	if trace.DependencyParents[dep][context] != 1 {
		t.Fatalf("expected parent attribution to survive resolved redaction, got %#v", trace.DependencyParents[dep])
	}
	if trace.DependencyEntrypoints[dep][context] != 1 {
		t.Fatalf("expected entrypoint attribution to survive resolved redaction, got %#v", trace.DependencyEntrypoints[dep])
	}
}

func TestLoadForRepoRejectsUnsafeJSDependencyValues(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("mkdir repo src: %v", err)
	}
	mainPath := filepath.Join(repo, "src", "main.js")
	if err := os.WriteFile(mainPath, []byte("console.log('x')\n"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}

	content :=
		`{"dependency":"fixture-dep","parent":"` + mainPath + `","entrypoint":"` + mainPath + `"}` + "\n" +
			`{"dependency":"fixture-dep/.env/private.js","parent":"` + mainPath + `","entrypoint":"` + mainPath + `"}` + "\n" +
			`{"dependency":"C:\\Users\\alice\\private.js","parent":"` + mainPath + `","entrypoint":"` + mainPath + `"}` + "\n" +
			`{"dependency":"../private.js","parent":"` + mainPath + `","entrypoint":"` + mainPath + `"}` + "\n" +
			`{"dependency":"@scope/pkg/index.js","parent":"` + mainPath + `","entrypoint":"` + mainPath + `"}` + "\n" +
			`{"dependency":"//server/share/private.js","module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"` + mainPath + `","entrypoint":"` + mainPath + `"}` + "\n"

	tracePath := testutil.WriteTempFile(t, filepath.Join("runtime", "trace.ndjson"), content)
	trace, err := LoadForRepo(tracePath, repo)
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}

	if got := trace.DependencyLoads["fixture-dep"]; got != 1 {
		t.Fatalf("expected safe bare dependency to survive, got %d", got)
	}
	if got := trace.DependencyLoads["@scope/pkg"]; got != 1 {
		t.Fatalf("expected safe scoped dependency to normalize to package ID, got %d", got)
	}
	if got := trace.DependencyLoads["lodash"]; got != 1 {
		t.Fatalf("expected unsafe dependency to fall back to safe module/resolved attribution, got %d", got)
	}
	for _, dep := range []string{"fixture-dep/.env/private.js", `C:\Users\alice\private.js`, "../private.js", "//server/share/private.js"} {
		if got := trace.DependencyLoads[dep]; got != 0 {
			t.Fatalf("expected unsafe dependency %q to be redacted, got load count %d", dep, got)
		}
	}
}

func TestLoadForRepoRedactsForeignAbsoluteRuntimeContexts(t *testing.T) {
	repo := t.TempDir()
	contexts := []string{
		"C:/Users/alice/private.js",
		`C:\Users\alice\private.js`,
		"C:Users/alice/private.js",
		"//server/share/alice/private.js",
		`\\server\share\alice\private.js`,
		"//./C:/Users/alice/private.js",
		`\\.\C:\Users\alice\private.js`,
		"//?/C:/Users/alice/private.js",
		`\\?\C:\Users\alice\private.js`,
		"//?/UNC/server/share/alice/private.js",
		`\\?\UNC\server\share\alice\private.js`,
		"/??/C:/Users/alice/private.js",
		`\??\C:\Users\alice\private.js`,
		"/Device/HarddiskVolume1/Users/alice/private.js",
		`\Device\HarddiskVolume1\Users\alice\private.js`,
		"https:/example.com/private.js",
		"src/../../Users/alice/private.js",
		"~/private/main.js",
		"pkg/.env/private.js",
		"pkg/~/.ssh/id_rsa",
	}

	var content strings.Builder
	for _, context := range contexts {
		line, err := json.Marshal(Event{
			Module:     lodashMapModule,
			Resolved:   "file:///repo/node_modules/lodash/map.js",
			Parent:     context,
			Entrypoint: context,
		})
		if err != nil {
			t.Fatalf("marshal runtime event for %q: %v", context, err)
		}
		content.Write(line)
		content.WriteByte('\n')
	}
	for _, event := range []Event{
		{Module: lodashMapModule, Parent: "lodash/fp.js", Entrypoint: "@scope/pkg/index.js"},
		{Module: lodashMapModule, Parent: "@scope/pkg/index.js", Entrypoint: "lodash/fp.js"},
	} {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal package-label runtime event: %v", err)
		}
		content.Write(line)
		content.WriteByte('\n')
	}

	tracePath := testutil.WriteTempFile(t, filepath.Join("runtime", "trace.ndjson"), content.String())
	trace, err := LoadForRepo(tracePath, repo)
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyParents["lodash"]; got["lodash/fp.js"] != 1 || got["@scope/pkg/index.js"] != 1 || len(got) != 2 {
		t.Fatalf("expected only package-style parent labels, got %#v", got)
	}
	if got := trace.DependencyEntrypoints["lodash"]; got["lodash/fp.js"] != 1 || got["@scope/pkg/index.js"] != 1 || len(got) != 2 {
		t.Fatalf("expected only package-style entrypoint labels, got %#v", got)
	}
}

func TestLoadForRepoRejectsOuterWhitespaceForPackageStyleContextsBeforeCacheLookup(t *testing.T) {
	repo := t.TempDir()
	content :=
		`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"node:fs","entrypoint":"@scope/pkg/index.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":" node:fs","entrypoint":" @scope/pkg/index.js "}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"node:fs ","entrypoint":"@scope/pkg/index.js\t"}` + "\n"

	tracePath := testutil.WriteTempFile(t, filepath.Join("runtime", "trace.ndjson"), content)
	trace, err := LoadForRepo(tracePath, repo)
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyParents["lodash"]; got["node:fs"] != 1 || len(got) != 1 {
		t.Fatalf("expected only clean node builtin parent label to survive, got %#v", got)
	}
	if got := trace.DependencyEntrypoints["lodash"]; got["@scope/pkg/index.js"] != 1 || len(got) != 1 {
		t.Fatalf("expected only clean scoped package entrypoint label to survive, got %#v", got)
	}
}

func TestLoadForRepoSkipsUnsafeNodeModulesHybridResolvedPaths(t *testing.T) {
	repo := t.TempDir()
	traceContent :=
		`{"resolved":"node_modules/fixture-dep/index.js","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n" +
			`{"resolved":"node_modules/fixture-dep/C:/Users/alice/private.mjs","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n" +
			`{"resolved":"node_modules/fixture-dep/https:/secret.mjs","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n" +
			`{"resolved":"node_modules/fixture-dep/.env/private.mjs","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n" +
			`{"resolved":"node_modules/fixture-dep/~/.ssh/id_rsa","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n" +
			`{"resolved":"packages/web/node_modules/fixture-dep/C:/Users/alice/private.mjs","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n"

	trace, err := loadTraceFromContentInRepo(t, repo, traceContent)
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if got := trace.DependencyLoads["fixture-dep"]; got != 1 {
		t.Fatalf("expected only safe fixture-dep load to survive, got %d from %#v", got, trace.DependencyLoads)
	}
	if got := trace.DependencyModules["fixture-dep"]; len(got) != 1 || got["fixture-dep/index.js"] != 1 {
		t.Fatalf("expected only safe runtime module, got %#v", got)
	}
	if got := trace.DependencySymbols["fixture-dep"]; len(got) != 1 || got["fixture-dep/index.js\x00index"] != 1 {
		t.Fatalf("expected only safe runtime symbol, got %#v", got)
	}
}

func TestLoadTraceCachesRuntimeContextNormalizationPerRawValue(t *testing.T) {
	repo := t.TempDir()
	mainPath := filepath.Join(repo, "src", "main.js")
	workerPath := filepath.Join(repo, "src", "worker.js")
	for _, path := range []string{mainPath, workerPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir repo src: %v", err)
		}
		if err := os.WriteFile(path, []byte("console.log('x')\n"), 0o600); err != nil {
			t.Fatalf("write repo file %q: %v", path, err)
		}
	}

	var calls []string
	content :=
		`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"src/worker.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/worker.js","entrypoint":"src/main.js"}` + "\n"
	tracePath := testutil.WriteTempFile(t, filepath.Join("runtime", "trace.ndjson"), content)

	trace, err := load(tracePath, traceLoadOptions{
		repoRoot:         repo,
		resolvedRepoRoot: resolvedRuntimeRepoRoot(repo),
		evalSymlinks: func(path string) (string, error) {
			calls = append(calls, filepath.Clean(path))
			return filepath.EvalSymlinks(path)
		},
	})
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if trace.DependencyParents["lodash"]["src/main.js"] != 2 {
		t.Fatalf("expected cached parent normalization to preserve both main.js counts, got %#v", trace.DependencyParents["lodash"])
	}
	if trace.DependencyParents["lodash"]["src/worker.js"] != 1 {
		t.Fatalf("expected cached parent normalization to preserve worker.js count, got %#v", trace.DependencyParents["lodash"])
	}
	if trace.DependencyEntrypoints["lodash"]["src/main.js"] != 2 {
		t.Fatalf("expected cached entrypoint normalization to preserve main.js counts, got %#v", trace.DependencyEntrypoints["lodash"])
	}
	if trace.DependencyEntrypoints["lodash"]["src/worker.js"] != 1 {
		t.Fatalf("expected cached entrypoint normalization to preserve worker.js count, got %#v", trace.DependencyEntrypoints["lodash"])
	}
	resolvedMainPath, err := filepath.EvalSymlinks(mainPath)
	if err != nil {
		t.Fatalf("resolve main path symlinks: %v", err)
	}
	resolvedWorkerPath, err := filepath.EvalSymlinks(workerPath)
	if err != nil {
		t.Fatalf("resolve worker path symlinks: %v", err)
	}
	if want := []string{filepath.Clean(resolvedMainPath), filepath.Clean(resolvedWorkerPath)}; len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("expected one filesystem probe per distinct raw context, got %#v want %#v", calls, want)
	}
}

func oversizedRuntimeTraceContent() string {
	const maxRuntimeTraceBytesForTest = 8 * 1024 * 1024

	line := "{\"module\":\"" + leftPadModule + "\"}\n"
	repeat := maxRuntimeTraceBytesForTest/len(line) + 1
	return strings.Repeat(line, repeat)
}
