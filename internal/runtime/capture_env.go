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

var (
	runtimeHookPathsOnce   sync.Once
	runtimeRequireHookPath string
	runtimeLoaderHookPath  string
	runtimeHookPathsErr    error
)

func withRuntimeTraceEnv(base []string, tracePath string) ([]string, error) {
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
	return fmt.Sprintf("--require=%s --loader=%s", requirePath, loaderPath), nil
}

func runtimeHookPaths() (string, string, error) {
	runtimeHookPathsOnce.Do(func() {
		runtimeRequireHookPath, runtimeLoaderHookPath, runtimeHookPathsErr = locateRuntimeHookPaths()
	})
	return runtimeRequireHookPath, runtimeLoaderHookPath, runtimeHookPathsErr
}

func locateRuntimeHookPaths() (string, string, error) {
	return locateRuntimeHookPathsInRoots(runtimeHookSearchRoots())
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

func runtimeHookSearchRoots() []string {
	seen := make(map[string]struct{})
	roots := make([]string, 0)
	addSearchPath := func(path string) {
		if path == "" {
			return
		}
		for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
			if !filepath.IsAbs(dir) {
				break
			}
			if _, ok := seen[dir]; !ok {
				seen[dir] = struct{}{}
				roots = append(roots, dir)
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}

	if executablePath, err := os.Executable(); err == nil {
		executableDir := filepath.Dir(executablePath)
		addSearchPath(executableDir)
		addSearchPath(filepath.Join(executableDir, "..", "share", "lopper"))
	}
	if _, filename, _, ok := goruntime.Caller(0); ok {
		addSearchPath(filepath.Dir(filename))
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
