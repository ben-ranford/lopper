package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
)

const runtimeRequireHookRelPath = "scripts/runtime/require-hook.cjs"
const runtimeLoaderHookRelPath = "scripts/runtime/loader.mjs"
const runtimePythonHookRelPath = "scripts/runtime/sitecustomize.py"

var (
	runtimeHookPathsOnce   sync.Once
	runtimeRequireHookPath string
	runtimeLoaderHookPath  string
	runtimeHookPathsErr    error

	runtimePythonHookDirOnce sync.Once
	runtimePythonHookDirPath string
	runtimePythonHookDirErr  error
)

var runtimeExecutablePath = os.Executable
var runtimeCaller = goruntime.Caller

func withRuntimeTraceEnv(base []string, tracePath string, provider CaptureProvider) ([]string, error) {
	switch normalizeCaptureProvider(provider) {
	case CaptureProviderNode:
		return withNodeRuntimeTraceEnv(base, tracePath)
	case CaptureProviderPython:
		return withPythonRuntimeTraceEnv(base, tracePath)
	default:
		return nil, fmt.Errorf("unsupported runtime capture provider %q", provider)
	}
}

func withNodeRuntimeTraceEnv(base []string, tracePath string) ([]string, error) {
	required, err := runtimeNodeHookOptions()
	if err != nil {
		return nil, fmt.Errorf("resolve runtime node hooks: %w", err)
	}

	existing := readEnvValue(base, "NODE_OPTIONS")
	updates := map[string]string{
		"LOPPER_RUNTIME_TRACE": tracePath,
	}
	nodeOptions := strings.TrimSpace(existing)
	if nodeOptions == "" {
		updates["NODE_OPTIONS"] = required
	} else {
		updates["NODE_OPTIONS"] = nodeOptions + " " + required
	}
	return mergeEnv(base, updates), nil
}

func withPythonRuntimeTraceEnv(base []string, tracePath string) ([]string, error) {
	hookDir, err := runtimePythonHookDirectory()
	if err != nil {
		return nil, fmt.Errorf("resolve runtime python hook: %w", err)
	}

	pythonPath := hookDir
	if existing := strings.TrimSpace(readEnvValue(base, "PYTHONPATH")); existing != "" {
		pythonPath += string(os.PathListSeparator) + existing
	}
	return mergeEnv(base, map[string]string{
		"LOPPER_RUNTIME_TRACE": tracePath,
		"PYTHONPATH":           pythonPath,
	}), nil
}

func mergeEnv(base []string, updates map[string]string) []string {
	merged := make(map[string]string, len(base)+len(updates))
	for _, item := range base {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		merged[parts[0]] = parts[1]
	}
	for key, value := range updates {
		merged[key] = value
	}
	items := make([]string, 0, len(merged))
	for key, value := range merged {
		items = append(items, key+"="+value)
	}
	return items
}

func readEnvValue(env []string, key string) string {
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if parts[0] == key {
			return parts[1]
		}
	}
	return ""
}

func runtimeNodeHookOptions() (string, error) {
	requirePath, loaderPath, err := runtimeHookPaths()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("--require=%s --loader=%s", quoteNodeOptionPath(requirePath), quoteNodeOptionPath(loaderPath)), nil
}

func quoteNodeOptionPath(path string) string {
	if !strings.ContainsAny(path, " \t\r\n\"") {
		return path
	}

	escaped := strings.ReplaceAll(path, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func runtimeHookPaths() (string, string, error) {
	runtimeHookPathsOnce.Do(func() {
		runtimeRequireHookPath, runtimeLoaderHookPath, runtimeHookPathsErr = locateRuntimeHookPaths()
	})
	return runtimeRequireHookPath, runtimeLoaderHookPath, runtimeHookPathsErr
}

func runtimePythonHookDirectory() (string, error) {
	runtimePythonHookDirOnce.Do(func() {
		runtimePythonHookDirPath, runtimePythonHookDirErr = locateRuntimePythonHookDirectory()
	})
	return runtimePythonHookDirPath, runtimePythonHookDirErr
}

func locateRuntimeHookPaths() (string, string, error) {
	return locateRuntimeHookPathsInRoots(runtimeHookSearchRoots())
}

func locateRuntimePythonHookDirectory() (string, error) {
	return locateRuntimePythonHookDirectoryInRoots(runtimeHookSearchRoots())
}

func locateRuntimeHookPathsInRoots(roots []string) (string, string, error) {
	for _, root := range roots {
		requirePath := filepath.Join(root, runtimeRequireHookRelPath)
		loaderPath := filepath.Join(root, runtimeLoaderHookRelPath)
		if !isRegularFile(requirePath) || !isRegularFile(loaderPath) {
			continue
		}
		return requirePath, loaderPath, nil
	}

	return "", "", fmt.Errorf("could not locate runtime hooks %q and %q", runtimeRequireHookRelPath, runtimeLoaderHookRelPath)
}

func locateRuntimePythonHookDirectoryInRoots(roots []string) (string, error) {
	for _, root := range roots {
		hookPath := filepath.Join(root, runtimePythonHookRelPath)
		if !isRegularFile(hookPath) {
			continue
		}
		return filepath.Dir(hookPath), nil
	}

	return "", fmt.Errorf("could not locate runtime python hook %q", runtimePythonHookRelPath)
}

func runtimeHookSearchRoots() []string {
	seen := make(map[string]struct{})
	roots := make([]string, 0)
	addSearchPath := func(path string) {
		if path == "" {
			return
		}
		dir := filepath.Clean(path)
		if !filepath.IsAbs(dir) {
			absDir, err := filepath.Abs(dir)
			if err != nil {
				return
			}
			dir = filepath.Clean(absDir)
		}
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		roots = append(roots, dir)
	}

	if executablePath, err := runtimeExecutablePath(); err == nil {
		executableDir := filepath.Dir(executablePath)
		addSearchPath(filepath.Join(executableDir, "share", "lopper"))
		addSearchPath(filepath.Join(executableDir, "..", "share", "lopper"))
	}
	if _, filename, _, ok := runtimeCaller(0); ok {
		addSearchPath(filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..")))
	}

	return roots
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}
