package scripts

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTraceHookSonarRegressions(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	source, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "trace-hook-interruption.py"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Contains(text, "urllib.error.URLError") {
		t.Fatal("URLError remains in an except tuple even though it is an OSError")
	}
	if !strings.Contains(text, "except OSError as error:") {
		t.Fatal("official archive download no longer catches OSError")
	}
	if strings.Count(text, `"setup.sh"`) != 1 || strings.Count(text, `b"installer\n"`) != 1 {
		t.Fatal("fixture installer literals are duplicated")
	}
}
