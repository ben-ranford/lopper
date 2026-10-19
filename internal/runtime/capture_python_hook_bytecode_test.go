package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Python 3.15 no longer sets module __cached__, so the hook must find its
// bytecode through the module spec. Removing __cached__ before execution
// reproduces that import state on every supported interpreter, and bytecode
// writing is forced so interpreter defaults cannot hide a leftover cache.
const runtimePythonHookWithoutCachedScript = `import importlib.util, sys
sys.dont_write_bytecode = False
spec = importlib.util.spec_from_file_location("sitecustomize", sys.argv[1])
module = importlib.util.module_from_spec(spec)
module.__dict__.pop("__cached__", None)
spec.loader.exec_module(module)
`

func TestRuntimePythonHookRemovesBytecodeWithoutModuleCached(t *testing.T) {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	hookDir, err := runtimePythonHookDirectory()
	if err != nil {
		t.Fatalf("runtime python hook directory: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(hookDir, "sitecustomize.py"))
	if err != nil {
		t.Fatalf("read runtime python hook: %v", err)
	}
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "sitecustomize.py")
	if err := os.WriteFile(hookPath, source, 0o600); err != nil {
		t.Fatalf("write runtime python hook copy: %v", err)
	}

	cmd := exec.Command(pythonPath, "-I", "-c", runtimePythonHookWithoutCachedScript, hookPath)
	cmd.Env = append(os.Environ(), "LOPPER_RUNTIME_TRACE="+filepath.Join(t.TempDir(), runtimeTraceNDJSON))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("load runtime python hook: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(dir, "__pycache__")); !os.IsNotExist(err) {
		t.Fatalf("expected runtime hook bytecode cache to be removed without module __cached__, stat error: %v", err)
	}
}
