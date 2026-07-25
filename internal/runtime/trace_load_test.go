package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestLoadTrace(t *testing.T) {
	trace, err := loadTraceFromContentInRepo(t, "/repo", `{"kind":"resolve","module":"`+lodashMapModule+`","resolved":"file:///repo/node_modules/lodash/map.js","parent":"file:///repo/src/index.js","entrypoint":"file:///repo/src/main.js"}`+"\n"+`{"kind":"require","module":"@scope/pkg/lib","resolved":"/repo/node_modules/@scope/pkg/lib/index.js","parent":"/repo/src/index.cjs","entrypoint":"/repo/src/start.cjs"}`+"\n")

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
	trace, err := loadTraceFromContentInRepo(t, "/repo", `{"language":"python","module":"requests.sessions","parent":"/repo/app.py","entrypoint":"/repo/app.py"}`+"\n"+`{"language":"python","dependency":"python-dateutil","module":"dateutil.parser","resolved":"/repo/.venv/lib/python3.12/site-packages/dateutil/parser.py"}`+"\n")
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

	original := resolveTraceRepoRoot
	calls := 0
	resolveTraceRepoRoot = func(repoPath string) string {
		calls++
		return original(repoPath)
	}
	defer func() {
		resolveTraceRepoRoot = original
	}()

	content :=
		`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n" +
			`{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"src/main.js","entrypoint":"src/main.js"}` + "\n"
	trace, err := loadTraceFromContentInRepo(t, repo, content)
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if calls != 1 {
		t.Fatalf("expected repo root to resolve once per load, got %d", calls)
	}
	if got := trace.DependencyParents["lodash"]["src/main.js"]; got != 2 {
		t.Fatalf("expected both parent samples to be counted, got %#v", trace.DependencyParents["lodash"])
	}
	if got := trace.DependencyEntrypoints["lodash"]["src/main.js"]; got != 2 {
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
	trace, err := loadTraceFromContentInRepo(t, t.TempDir(), `{"module":"lodash/map","resolved":"file:///repo/node_modules/lodash/map.js","parent":"node:internal/modules/cjs/loader","entrypoint":"lodash/map"}`+"\n")
	if err != nil {
		t.Fatalf(loadTraceErrFmt, err)
	}
	if trace.DependencyParents["lodash"]["node:internal/modules/cjs/loader"] != 1 {
		t.Fatalf("expected package-style parent label to be preserved, got %#v", trace.DependencyParents["lodash"])
	}
	if trace.DependencyEntrypoints["lodash"]["lodash/map"] != 1 {
		t.Fatalf("expected package-style entrypoint label to be preserved, got %#v", trace.DependencyEntrypoints["lodash"])
	}
}

func oversizedRuntimeTraceContent() string {
	const maxRuntimeTraceBytesForTest = 8 * 1024 * 1024

	line := "{\"module\":\"" + leftPadModule + "\"}\n"
	repeat := maxRuntimeTraceBytesForTest/len(line) + 1
	return strings.Repeat(line, repeat)
}
