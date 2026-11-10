package scripts

import (
	"strings"
	"testing"
)

func TestVSCodeSmokeRepairsDecompressorBeforeDependencyImport(t *testing.T) {
	t.Parallel()
	source := readConfig(t, "extensions/vscode-lopper/src/test/runTest.mjs")
	patch := strings.Index(source, "await patchTestElectronDecompressor();")
	load := strings.Index(source, `await import("@vscode/test-electron")`)
	if patch < 0 || load < 0 || patch >= load {
		t.Fatal("VS Code smoke must repair download cleanup before loading test-electron")
	}
	if strings.Contains(source, `from "@vscode/test-electron"`) {
		t.Fatal("a static test-electron import would load the broken helper before its repair")
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	readJSONConfig(t, "extensions/vscode-lopper/package.json", &manifest)
	if !strings.Contains(manifest.Scripts["test:e2e"], "./src/test/decompressorLifecycle.test.mjs") {
		t.Fatal("VS Code smoke must execute the download lifecycle regression tests")
	}
}
