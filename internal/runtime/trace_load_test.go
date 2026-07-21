package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
