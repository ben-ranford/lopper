package runtime

import (
	"errors"
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
const runtimeRepoRootEnvKey = "LOPPER_RUNTIME_REPO_ROOT"

type runtimeExecutablePathFunc func() (string, error)
type runtimeCallerFunc func(skip int) (uintptr, string, int, bool)

type runtimeHookPathResolver struct {
	executablePath runtimeExecutablePathFunc
	caller         runtimeCallerFunc

	hookPathsOnce sync.Once
	hookPaths     struct {
		requirePath string
		loaderPath  string
		err         error
	}

	pythonHookDirOnce sync.Once
	pythonHookDir     struct {
		path string
		err  error
	}
}

var defaultRuntimeHookPathResolver = newRuntimeHookPathResolver(os.Executable, goruntime.Caller)
var runtimeHookFileRead = os.ReadFile
var runtimeHookFileWrite = os.WriteFile
var runtimeHookDirMkdirTemp = os.MkdirTemp
var runtimeHookDirSeal = os.Chmod
var runtimeHookDirChmod = os.Chmod
var runtimeHookDirRemoveAll = os.RemoveAll

func newRuntimeHookPathResolver(executablePath runtimeExecutablePathFunc, caller runtimeCallerFunc) *runtimeHookPathResolver {
	if executablePath == nil {
		executablePath = os.Executable
	}
	if caller == nil {
		caller = goruntime.Caller
	}
	return &runtimeHookPathResolver{
		executablePath: executablePath,
		caller:         caller,
	}
}

func withRuntimeTraceEnv(base []string, tracePath string, provider CaptureProvider, repoPath string) ([]string, error) {
	return withRuntimeTraceEnvForResolver(base, tracePath, provider, repoPath, defaultRuntimeHookPathResolver)
}

func withRuntimeTraceEnvForResolver(base []string, tracePath string, provider CaptureProvider, repoPath string, resolver *runtimeHookPathResolver) ([]string, error) {
	switch normalizeCaptureProvider(provider) {
	case CaptureProviderNode:
		return withNodeRuntimeTraceEnvForResolver(base, tracePath, repoPath, resolver)
	case CaptureProviderPython:
		return withPythonRuntimeTraceEnvForResolver(base, tracePath, repoPath, resolver)
	default:
		return nil, fmt.Errorf("unsupported runtime capture provider %q", provider)
	}
}

func runtimeTraceEnvForCapture(base []string, tracePath string, provider CaptureProvider, repoPath string, resolver *runtimeHookPathResolver) ([]string, func() error, error) {
	switch normalizeCaptureProvider(provider) {
	case CaptureProviderNode:
		env, err := withNodeRuntimeTraceEnvForResolver(base, tracePath, repoPath, resolver)
		return env, nil, err
	case CaptureProviderPython:
		return withPreparedPythonRuntimeTraceEnvForResolver(base, tracePath, repoPath, resolver)
	default:
		return nil, nil, fmt.Errorf("unsupported runtime capture provider %q", provider)
	}
}

func withNodeRuntimeTraceEnv(base []string, tracePath string, repoPath string) ([]string, error) {
	return withNodeRuntimeTraceEnvForResolver(base, tracePath, repoPath, defaultRuntimeHookPathResolver)
}

func withNodeRuntimeTraceEnvForResolver(base []string, tracePath string, repoPath string, resolver *runtimeHookPathResolver) ([]string, error) {
	required, err := resolver.runtimeNodeHookOptions()
	if err != nil {
		return nil, fmt.Errorf("resolve runtime node hooks: %w", err)
	}

	existing := readEnvValue(base, "NODE_OPTIONS")
	updates := map[string]string{
		"LOPPER_RUNTIME_TRACE": tracePath,
		runtimeRepoRootEnvKey:  lexicalRuntimeRepoRoot(repoPath),
	}
	nodeOptions := strings.TrimSpace(existing)
	if nodeOptions == "" {
		updates["NODE_OPTIONS"] = required
	} else {
		updates["NODE_OPTIONS"] = nodeOptions + " " + required
	}
	return mergeEnv(base, updates), nil
}

func withPythonRuntimeTraceEnv(base []string, tracePath string, repoPath string) ([]string, error) {
	return withPythonRuntimeTraceEnvForResolver(base, tracePath, repoPath, defaultRuntimeHookPathResolver)
}

func withPythonRuntimeTraceEnvForResolver(base []string, tracePath string, repoPath string, resolver *runtimeHookPathResolver) ([]string, error) {
	hookDir, err := resolver.runtimePythonHookDirectory()
	if err != nil {
		return nil, fmt.Errorf("resolve runtime python hook: %w", err)
	}

	return withPythonRuntimeTraceEnvForHookDir(base, tracePath, repoPath, hookDir), nil
}

func withPreparedPythonRuntimeTraceEnvForResolver(base []string, tracePath string, repoPath string, resolver *runtimeHookPathResolver) ([]string, func() error, error) {
	hookDir, cleanup, err := stagePythonRuntimeHookDirectoryForResolver(resolver)
	if err != nil {
		return nil, nil, fmt.Errorf("stage runtime python hook: %w", err)
	}
	return withPythonRuntimeTraceEnvForHookDir(base, tracePath, repoPath, hookDir), cleanup, nil
}

func withPythonRuntimeTraceEnvForHookDir(base []string, tracePath string, repoPath string, hookDir string) []string {
	pythonPath := hookDir
	if existing := strings.TrimSpace(readEnvValue(base, "PYTHONPATH")); existing != "" {
		pythonPath += string(os.PathListSeparator) + existing
	}
	return mergeEnv(base, map[string]string{
		"LOPPER_RUNTIME_TRACE": tracePath,
		runtimeRepoRootEnvKey:  lexicalRuntimeRepoRoot(repoPath),
		"PYTHONPATH":           pythonPath,
	})
}

func lexicalRuntimeRepoRoot(repoPath string) string {
	trimmed := strings.TrimSpace(repoPath)
	if trimmed == "" {
		return ""
	}
	absPath, err := filepath.Abs(trimmed)
	if err == nil && strings.TrimSpace(absPath) != "" {
		return filepath.Clean(absPath)
	}
	return filepath.Clean(trimmed)
}

func resolvedRuntimeRepoRoot(repoPath string) string {
	trimmed := strings.TrimSpace(repoPath)
	if trimmed == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(trimmed)
	if err == nil && strings.TrimSpace(resolved) != "" {
		return filepath.Clean(resolved)
	}
	absPath, err := filepath.Abs(trimmed)
	if err == nil && strings.TrimSpace(absPath) != "" {
		return filepath.Clean(absPath)
	}
	return filepath.Clean(trimmed)
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

func quoteNodeOptionPath(path string) string {
	if !strings.ContainsAny(path, " \t\r\n\"") {
		return path
	}

	escaped := strings.ReplaceAll(path, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func runtimeHookPaths() (string, string, error) {
	return defaultRuntimeHookPathResolver.runtimeHookPaths()
}

func runtimePythonHookDirectory() (string, error) {
	return defaultRuntimeHookPathResolver.runtimePythonHookDirectory()
}

func stagePythonRuntimeHookDirectory() (string, func() error, error) {
	return stagePythonRuntimeHookDirectoryForResolver(defaultRuntimeHookPathResolver)
}

func locateRuntimeHookPaths() (string, string, error) {
	return locateRuntimeHookPathsInRoots(defaultRuntimeHookPathResolver.runtimeHookSearchRoots())
}

func locateRuntimePythonHookDirectory() (string, error) {
	return locateRuntimePythonHookDirectoryInRoots(defaultRuntimeHookPathResolver.runtimeHookSearchRoots())
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
	return defaultRuntimeHookPathResolver.runtimeHookSearchRoots()
}

func (r *runtimeHookPathResolver) runtimeNodeHookOptions() (string, error) {
	requirePath, loaderPath, err := r.runtimeHookPaths()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("--require=%s --loader=%s", quoteNodeOptionPath(requirePath), quoteNodeOptionPath(loaderPath)), nil
}

func (r *runtimeHookPathResolver) runtimeHookPaths() (string, string, error) {
	r.hookPathsOnce.Do(func() {
		r.hookPaths.requirePath, r.hookPaths.loaderPath, r.hookPaths.err = locateRuntimeHookPathsInRoots(r.runtimeHookSearchRoots())
	})
	return r.hookPaths.requirePath, r.hookPaths.loaderPath, r.hookPaths.err
}

func (r *runtimeHookPathResolver) runtimePythonHookDirectory() (string, error) {
	r.pythonHookDirOnce.Do(func() {
		r.pythonHookDir.path, r.pythonHookDir.err = locateRuntimePythonHookDirectoryInRoots(r.runtimeHookSearchRoots())
	})
	return r.pythonHookDir.path, r.pythonHookDir.err
}

func stagePythonRuntimeHookDirectoryForResolver(resolver *runtimeHookPathResolver) (string, func() error, error) {
	hookDir, err := resolver.runtimePythonHookDirectory()
	if err != nil {
		return "", nil, err
	}
	sourcePath := filepath.Join(hookDir, filepath.Base(runtimePythonHookRelPath))
	content, err := runtimeHookFileRead(sourcePath)
	if err != nil {
		return "", nil, fmt.Errorf("read runtime python hook: %w", sanitizeRuntimeHookStageError(err, sourcePath))
	}
	stagedDir, err := runtimeHookDirMkdirTemp("", "lopper-python-hook-")
	if err != nil {
		return "", nil, fmt.Errorf("create runtime python hook dir: %w", sanitizeRuntimeHookStageTempDirError(err))
	}
	cleanup := func() error {
		var cleanupErrs []error
		if err := runtimeHookDirChmod(stagedDir, 0o700); err != nil && !os.IsNotExist(err) {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("chmod staged runtime python hook dir: %w", sanitizeRuntimeHookStageError(err, stagedDir)))
		}
		if err := runtimeHookDirRemoveAll(stagedDir); err != nil && !os.IsNotExist(err) {
			cleanupErrs = append(cleanupErrs, fmt.Errorf("remove staged runtime python hook dir: %w", sanitizeRuntimeHookStageError(err, stagedDir)))
		}
		return errors.Join(cleanupErrs...)
	}
	stagedPath := filepath.Join(stagedDir, filepath.Base(runtimePythonHookRelPath))
	if err := runtimeHookFileWrite(stagedPath, content, 0o444); err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return "", nil, errors.Join(fmt.Errorf("write staged runtime python hook: %w", sanitizeRuntimeHookStageError(err, stagedPath)), fmt.Errorf("cleanup staged runtime python hook: %w", cleanupErr))
		}
		return "", nil, fmt.Errorf("write staged runtime python hook: %w", sanitizeRuntimeHookStageError(err, stagedPath))
	}
	if err := runtimeHookDirSeal(stagedDir, 0o555); err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return "", nil, errors.Join(fmt.Errorf("seal staged runtime python hook dir: %w", sanitizeRuntimeHookStageError(err, stagedDir)), fmt.Errorf("cleanup staged runtime python hook: %w", cleanupErr))
		}
		return "", nil, fmt.Errorf("seal staged runtime python hook dir: %w", sanitizeRuntimeHookStageError(err, stagedDir))
	}
	return stagedDir, cleanup, nil
}

type runtimeHookStageSanitizedError struct {
	message string
	err     error
}

func (e *runtimeHookStageSanitizedError) Error() string {
	return e.message
}

func (e *runtimeHookStageSanitizedError) Unwrap() error {
	return e.err
}

func sanitizeRuntimeHookStageError(err error, sensitivePaths ...string) error {
	if err == nil {
		return nil
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return &os.PathError{
			Op:   pathErr.Op,
			Path: sanitizeRuntimeHookStageText(pathErr.Path, sensitivePaths...),
			Err:  sanitizeRuntimeHookStageError(pathErr.Err, sensitivePaths...),
		}
	}
	message := sanitizeRuntimeHookStageText(err.Error(), sensitivePaths...)
	if message == err.Error() {
		return err
	}
	return &runtimeHookStageSanitizedError{message: message, err: err}
}

func sanitizeRuntimeHookStageTempDirError(err error) error {
	if err == nil {
		return nil
	}
	sensitivePaths := []string{os.TempDir()}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && pathErr.Path != "" {
		sensitivePaths = append(sensitivePaths, pathErr.Path)
	}
	return sanitizeRuntimeHookStageError(err, sensitivePaths...)
}

func sanitizeRuntimeHookStageText(text string, sensitivePaths ...string) string {
	sanitized := text
	for _, sensitivePath := range sensitivePaths {
		for _, candidate := range runtimeHookSensitivePathVariants(sensitivePath) {
			sanitized = strings.ReplaceAll(sanitized, candidate, "<path>")
		}
	}
	return sanitized
}

func runtimeHookSensitivePathVariants(path string) []string {
	if path == "" {
		return nil
	}
	variants := []string{path}
	cleanPath := filepath.Clean(path)
	if cleanPath != path {
		variants = append(variants, cleanPath)
	}
	slashPath := filepath.ToSlash(path)
	if slashPath != path && slashPath != cleanPath {
		variants = append(variants, slashPath)
	}
	return variants
}

func (r *runtimeHookPathResolver) runtimeHookSearchRoots() []string {
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

	if executablePath, err := r.executablePath(); err == nil {
		executableDir := filepath.Dir(executablePath)
		addSearchPath(filepath.Join(executableDir, "share", "lopper"))
		addSearchPath(filepath.Join(executableDir, "..", "share", "lopper"))
	}
	if _, filename, _, ok := r.caller(0); ok {
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
