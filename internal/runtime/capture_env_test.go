package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWithRuntimeTraceEnv(t *testing.T) {
	tracePath := "/tmp/runtime.ndjson"
	requirePath, loaderPath, err := runtimeHookPaths()
	if err != nil {
		t.Fatalf("runtime hook paths: %v", err)
	}

	env, err := withRuntimeTraceEnv([]string{"NODE_OPTIONS=--max-old-space-size=4096", "PATH=/usr/bin"}, tracePath, CaptureProviderNode, "/repo")
	if err != nil {
		t.Fatalf("with runtime trace env: %v", err)
	}

	assertEnvEntryValue(t, env, "LOPPER_RUNTIME_TRACE", tracePath)
	assertEnvEntryValue(t, env, runtimeRepoRootEnvKey, lexicalRuntimeRepoRoot("/repo"))
	assertNodeOptionsEntry(t, env, requirePath, loaderPath)
}

func TestWithPythonRuntimeTraceEnv(t *testing.T) {
	tracePath := "/tmp/python-runtime.ndjson"
	hookDir, err := runtimePythonHookDirectory()
	if err != nil {
		t.Fatalf("runtime python hook directory: %v", err)
	}

	env, err := withRuntimeTraceEnv([]string{"PYTHONDONTWRITEBYTECODE=0", "PYTHONPATH=/existing/path", "NODE_OPTIONS=--max-old-space-size=4096"}, tracePath, CaptureProviderPython, "/repo")
	if err != nil {
		t.Fatalf("with python runtime trace env: %v", err)
	}

	assertEnvEntryValue(t, env, "LOPPER_RUNTIME_TRACE", tracePath)
	assertEnvEntryValue(t, env, runtimeRepoRootEnvKey, lexicalRuntimeRepoRoot("/repo"))
	if value, ok := lookupEnvEntry(env, "PYTHONDONTWRITEBYTECODE"); !ok || value != "0" {
		t.Fatalf("expected PYTHONDONTWRITEBYTECODE to be preserved, got %q (set=%v)", value, ok)
	}
	pythonPath, ok := lookupEnvEntry(env, "PYTHONPATH")
	if !ok {
		t.Fatalf("expected PYTHONPATH to be set")
	}
	wantPythonPath := hookDir + string(os.PathListSeparator) + "/existing/path"
	if pythonPath != wantPythonPath {
		t.Fatalf("expected PYTHONPATH %q, got %q", wantPythonPath, pythonPath)
	}
	if nodeOptions, ok := lookupEnvEntry(env, "NODE_OPTIONS"); !ok || nodeOptions != "--max-old-space-size=4096" {
		t.Fatalf("expected NODE_OPTIONS to be preserved without Python changes, got %q", nodeOptions)
	}
}

func TestWithPythonRuntimeTraceEnvWithoutExistingPythonPath(t *testing.T) {
	tracePath := "/tmp/python-runtime.ndjson"
	hookDir, err := runtimePythonHookDirectory()
	if err != nil {
		t.Fatalf("runtime python hook directory: %v", err)
	}

	env, err := withPythonRuntimeTraceEnv([]string{"PATH=/usr/bin"}, tracePath, "/repo")
	if err != nil {
		t.Fatalf("with python runtime trace env: %v", err)
	}

	assertEnvEntryValue(t, env, "LOPPER_RUNTIME_TRACE", tracePath)
	assertEnvEntryValue(t, env, "PYTHONPATH", hookDir)
	assertEnvEntryValue(t, env, runtimeRepoRootEnvKey, lexicalRuntimeRepoRoot("/repo"))
	if _, ok := lookupEnvEntry(env, "PYTHONDONTWRITEBYTECODE"); ok {
		t.Fatalf("expected PYTHONDONTWRITEBYTECODE to remain unset")
	}
}

func TestStagePythonRuntimeHookDirectory(t *testing.T) {
	stagedDir, cleanup, err := stagePythonRuntimeHookDirectory()
	if err != nil {
		t.Fatalf("stage runtime python hook directory: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup staged runtime python hook directory: %v", err)
		}
	}()

	info, err := os.Stat(stagedDir)
	if err != nil {
		t.Fatalf("stat staged runtime python hook directory: %v", err)
	}
	if info.Mode().Perm() != 0o555 {
		t.Fatalf("expected staged runtime python hook dir perms 0555, got %o", info.Mode().Perm())
	}
	hookPath := filepath.Join(stagedDir, filepath.Base(runtimePythonHookRelPath))
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("stat staged runtime python hook: %v", err)
	}
}

func TestStagePythonRuntimeHookDirectoryCleanupReturnsError(t *testing.T) {
	stagedDir, cleanup, err := stagePythonRuntimeHookDirectory()
	if err != nil {
		t.Fatalf("stage runtime python hook directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(stagedDir, 0o700); err != nil && !os.IsNotExist(err) {
			t.Fatalf("cleanup staged runtime python hook directory chmod: %v", err)
		}
		if err := os.RemoveAll(stagedDir); err != nil && !os.IsNotExist(err) {
			t.Fatalf("cleanup staged runtime python hook directory remove: %v", err)
		}
	})

	previousChmod := runtimeHookDirChmod
	previousRemoveAll := runtimeHookDirRemoveAll
	removeAllCalled := false
	runtimeHookDirChmod = func(path string, mode os.FileMode) error {
		if path != stagedDir {
			return previousChmod(path, mode)
		}
		return fmt.Errorf("forced chmod failure")
	}
	runtimeHookDirRemoveAll = func(path string) error {
		if path != stagedDir {
			return previousRemoveAll(path)
		}
		removeAllCalled = true
		return nil
	}
	t.Cleanup(func() {
		runtimeHookDirChmod = previousChmod
		runtimeHookDirRemoveAll = previousRemoveAll
	})

	err = cleanup()
	if err == nil || !strings.Contains(err.Error(), "chmod staged runtime python hook dir") {
		t.Fatalf("expected staged hook cleanup chmod error, got %v", err)
	}
	if !removeAllCalled {
		t.Fatal("expected cleanup to remove staged runtime python hook dir after chmod failure")
	}
}

func TestStagePythonRuntimeHookDirectorySanitizesReadSourcePathErrors(t *testing.T) {
	previous := runtimeHookFileRead
	runtimeHookFileRead = func(path string) ([]byte, error) {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	}
	t.Cleanup(func() {
		runtimeHookFileRead = previous
	})

	assertStagePythonRuntimeHookDirectoryError(t, "read runtime python hook", "open <path>")
}

func TestStagePythonRuntimeHookDirectorySanitizesWritePathErrors(t *testing.T) {
	previous := runtimeHookFileWrite
	runtimeHookFileWrite = func(path string, _ []byte, _ os.FileMode) error {
		return &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	}
	t.Cleanup(func() {
		runtimeHookFileWrite = previous
	})

	assertStagePythonRuntimeHookDirectoryError(t, "write staged runtime python hook", "open <path>")
}

func TestStagePythonRuntimeHookDirectorySanitizesTempDirPathErrors(t *testing.T) {
	previousMkdirTemp := runtimeHookDirMkdirTemp
	tempRoot := filepath.Join(string(os.PathSeparator), "tmp", "lopper-path-neutral-test")
	runtimeHookDirMkdirTemp = func(dir, pattern string) (string, error) {
		return "", &os.PathError{
			Op:   "mkdirtemp",
			Path: filepath.Join(tempRoot, pattern+"123456789"),
			Err:  os.ErrPermission,
		}
	}
	t.Cleanup(func() {
		runtimeHookDirMkdirTemp = previousMkdirTemp
	})

	err := stagePythonRuntimeHookDirectoryError(t)
	assertRuntimeHookStagePathNeutralError(t, err, "create runtime python hook dir", "mkdirtemp <path>")
	if strings.Contains(err.Error(), tempRoot) {
		t.Fatalf("expected staged dir creation error to exclude temp root %q, got %q", tempRoot, err)
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected staged dir creation error to preserve errors.Is(..., os.ErrPermission), got %v", err)
	}
}

func TestStagePythonRuntimeHookDirectorySanitizesSealPathErrors(t *testing.T) {
	previous := runtimeHookDirSeal
	runtimeHookDirSeal = func(path string, _ os.FileMode) error {
		return &os.PathError{Op: "chmod", Path: path, Err: os.ErrPermission}
	}
	t.Cleanup(func() {
		runtimeHookDirSeal = previous
	})

	assertStagePythonRuntimeHookDirectoryError(t, "seal staged runtime python hook dir", "chmod <path>")
}

func TestStagePythonRuntimeHookDirectoryCleanupSanitizesChmodAndRemoveErrors(t *testing.T) {
	stagedDir, cleanup, err := stagePythonRuntimeHookDirectory()
	if err != nil {
		t.Fatalf("stage runtime python hook directory: %v", err)
	}
	t.Cleanup(func() {
		if chmodErr := os.Chmod(stagedDir, 0o700); chmodErr != nil && !os.IsNotExist(chmodErr) {
			t.Fatalf("cleanup staged runtime python hook directory chmod: %v", chmodErr)
		}
		if removeErr := os.RemoveAll(stagedDir); removeErr != nil && !os.IsNotExist(removeErr) {
			t.Fatalf("cleanup staged runtime python hook directory remove: %v", removeErr)
		}
	})

	previousChmod := runtimeHookDirChmod
	previousRemoveAll := runtimeHookDirRemoveAll
	runtimeHookDirChmod = func(path string, mode os.FileMode) error {
		if path != stagedDir {
			return previousChmod(path, mode)
		}
		return &os.PathError{Op: "chmod", Path: path, Err: os.ErrPermission}
	}
	runtimeHookDirRemoveAll = func(path string) error {
		if path != stagedDir {
			return previousRemoveAll(path)
		}
		return &os.PathError{Op: "remove", Path: path, Err: os.ErrPermission}
	}
	t.Cleanup(func() {
		runtimeHookDirChmod = previousChmod
		runtimeHookDirRemoveAll = previousRemoveAll
	})

	err = cleanup()
	assertRuntimeHookStagePathNeutralError(t, err, "chmod staged runtime python hook dir", "chmod <path>")
	assertRuntimeHookStagePathNeutralError(t, err, "remove staged runtime python hook dir", "remove <path>")
}

func stagePythonRuntimeHookDirectoryError(t *testing.T) error {
	t.Helper()

	_, _, err := stagePythonRuntimeHookDirectoryForResolver(newRuntimeHookPathResolver(nil, nil))
	return err
}

func assertStagePythonRuntimeHookDirectoryError(t *testing.T, requiredSubstrings ...string) {
	t.Helper()

	assertRuntimeHookStagePathNeutralError(t, stagePythonRuntimeHookDirectoryError(t), requiredSubstrings...)
}

func TestWithPreparedPythonRuntimeTraceEnvForResolverWrapsStagingError(t *testing.T) {
	sentinel := errors.New("python hook lookup failed")
	resolver := newFakeRuntimeHookPathResolver(func() (string, error) { return "", nil }, func(skip int) (uintptr, string, int, bool) { return 0, "", 0, false })
	resolver.pythonHookDir.err = sentinel
	resolver.pythonHookDirOnce.Do(func() {})

	env, cleanup, err := withPreparedPythonRuntimeTraceEnvForResolver([]string{"PATH=/usr/bin"}, "/tmp/python-runtime.ndjson", "/repo", resolver)
	if len(env) != 0 {
		t.Fatalf("expected nil env on staged hook error, got %v", env)
	}
	if cleanup != nil {
		t.Fatal("expected nil cleanup on staged hook error")
	}
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "stage runtime python hook") {
		t.Fatalf("expected wrapped staged hook error for %v, got %v", sentinel, err)
	}
}

func TestStagePythonRuntimeHookDirectoryForResolverJoinsSanitizedWriteAndCleanupErrors(t *testing.T) {
	resolver := newRuntimeHookPathResolver(nil, nil)

	previousWriteFile := runtimeHookFileWrite
	previousChmod := runtimeHookDirChmod
	previousRemoveAll := runtimeHookDirRemoveAll
	runtimeHookFileWrite = func(path string, _ []byte, _ os.FileMode) error {
		return &os.PathError{Op: "open", Path: path, Err: os.ErrPermission}
	}
	runtimeHookDirChmod = func(path string, _ os.FileMode) error {
		return &os.PathError{Op: "chmod", Path: path, Err: os.ErrPermission}
	}
	runtimeHookDirRemoveAll = func(path string) error {
		return &os.PathError{Op: "remove", Path: path, Err: os.ErrPermission}
	}
	t.Cleanup(func() {
		runtimeHookFileWrite = previousWriteFile
		runtimeHookDirChmod = previousChmod
		runtimeHookDirRemoveAll = previousRemoveAll
	})

	_, _, err := stagePythonRuntimeHookDirectoryForResolver(resolver)
	assertRuntimeHookStagePathNeutralError(t, err, "write staged runtime python hook", "cleanup staged runtime python hook", "open <path>", "chmod <path>", "remove <path>")
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected staged write error to preserve os.ErrPermission, got %v", err)
	}
}

func TestStagePythonRuntimeHookDirectoryForResolverJoinsSanitizedSealAndCleanupErrors(t *testing.T) {
	resolver := newRuntimeHookPathResolver(nil, nil)

	previousSealDir := runtimeHookDirSeal
	previousChmod := runtimeHookDirChmod
	previousRemoveAll := runtimeHookDirRemoveAll
	runtimeHookDirSeal = func(path string, _ os.FileMode) error {
		return &os.PathError{Op: "chmod", Path: path, Err: os.ErrPermission}
	}
	runtimeHookDirChmod = func(path string, _ os.FileMode) error {
		return &os.PathError{Op: "chmod", Path: path, Err: os.ErrPermission}
	}
	runtimeHookDirRemoveAll = func(path string) error {
		return &os.PathError{Op: "remove", Path: path, Err: os.ErrPermission}
	}
	t.Cleanup(func() {
		runtimeHookDirSeal = previousSealDir
		runtimeHookDirChmod = previousChmod
		runtimeHookDirRemoveAll = previousRemoveAll
	})

	_, _, err := stagePythonRuntimeHookDirectoryForResolver(resolver)
	assertRuntimeHookStagePathNeutralError(t, err, "seal staged runtime python hook dir", "cleanup staged runtime python hook", "chmod <path>", "remove <path>")
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected staged seal error to preserve os.ErrPermission, got %v", err)
	}
}

func TestSanitizeRuntimeHookStageErrorReturnsNilForNilInput(t *testing.T) {
	if err := sanitizeRuntimeHookStageError(nil, "/tmp/private"); err != nil {
		t.Fatalf("expected nil sanitize error result, got %v", err)
	}
}

func TestSanitizeRuntimeHookStageErrorWrapsSanitizedMessage(t *testing.T) {
	sensitivePath := filepath.Join(string(os.PathSeparator), "tmp", "private-hook.py")
	cause := fmt.Errorf("chmod %s: permission denied", sensitivePath)

	err := sanitizeRuntimeHookStageError(cause, sensitivePath)

	var sanitized *runtimeHookStageSanitizedError
	if !errors.As(err, &sanitized) {
		t.Fatalf("expected sanitized runtime hook error wrapper, got %T", err)
	}
	if sanitized.Error() != "chmod <path>: permission denied" {
		t.Fatalf("expected sanitized message, got %q", sanitized.Error())
	}
	if !errors.Is(sanitized.Unwrap(), cause) {
		t.Fatalf("expected sanitized error to unwrap original cause, got %v", sanitized.Unwrap())
	}
}

func TestSanitizeRuntimeHookStageTempDirErrorReturnsNilForNilInput(t *testing.T) {
	if err := sanitizeRuntimeHookStageTempDirError(nil); err != nil {
		t.Fatalf("expected nil temp dir sanitize result, got %v", err)
	}
}

func TestRuntimeHookSensitivePathVariantsReturnsCleanVariant(t *testing.T) {
	path := string(os.PathSeparator) + filepath.Join("tmp", "nested") + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "hook.py"
	variants := runtimeHookSensitivePathVariants(path)
	want := []string{path, filepath.Clean(path)}
	if !reflect.DeepEqual(variants, want) {
		t.Fatalf("expected raw and clean variants, got %v", variants)
	}
}

func TestLexicalRuntimeRepoRootPreservesAlias(t *testing.T) {
	if got := lexicalRuntimeRepoRoot(" \t "); got != "" {
		t.Fatalf("expected blank repo root to remain blank, got %q", got)
	}

	root := t.TempDir()
	realRepo := filepath.Join(root, "repo-real")
	aliasRepo := filepath.Join(root, "repo-alias")
	if err := os.MkdirAll(realRepo, 0o750); err != nil {
		t.Fatalf("mkdir real repo: %v", err)
	}
	if err := os.Symlink(realRepo, aliasRepo); err != nil {
		t.Skipf("repo symlinks unavailable: %v", err)
	}
	if got := lexicalRuntimeRepoRoot(aliasRepo); got != aliasRepo {
		t.Fatalf("expected lexical repo alias %q, got %q", aliasRepo, got)
	}
	if got := lexicalRuntimeRepoRoot(aliasRepo); got == resolvedRuntimeRepoRoot(aliasRepo) {
		t.Fatalf("expected lexical repo alias to differ from resolved root, got %q", got)
	}
}

func TestResolvedRuntimeRepoRootFallsBackToAbsolutePathWhenSymlinkResolutionFails(t *testing.T) {
	root := t.TempDir()
	missingPath := filepath.Join(root, "missing", "..", "repo")

	got := resolvedRuntimeRepoRoot(missingPath)
	want, err := filepath.Abs(missingPath)
	if err != nil {
		t.Fatalf("abs missing repo path: %v", err)
	}
	want = filepath.Clean(want)
	if got != want {
		t.Fatalf("expected resolved repo root %q, got %q", want, got)
	}
}

func TestResolvedRuntimeRepoRootReturnsBlankForBlankInput(t *testing.T) {
	if got := resolvedRuntimeRepoRoot(" \n\t "); got != "" {
		t.Fatalf("expected blank repo root to remain blank, got %q", got)
	}
}

func TestRuntimeCaptureProviderValidationBranches(t *testing.T) {
	if got := normalizeCaptureProvider(""); got != CaptureProviderNode {
		t.Fatalf("expected default provider to normalize to node, got %q", got)
	}
	if got := normalizeCaptureProvider(CaptureProviderPython); got != CaptureProviderPython {
		t.Fatalf("expected python provider to normalize to python, got %q", got)
	}
	if got := normalizeCaptureProvider("ruby"); got != "" {
		t.Fatalf("expected unsupported provider to normalize empty, got %q", got)
	}

	if _, err := withRuntimeTraceEnv(nil, "/tmp/runtime.ndjson", "ruby", "/repo"); err == nil || !strings.Contains(err.Error(), "unsupported runtime capture provider") {
		t.Fatalf("expected unsupported provider env error, got %v", err)
	}
	if _, err := resolveCapturePlan(CaptureRequest{RepoPath: t.TempDir(), Command: "npm test", Provider: "ruby"}); err == nil || !strings.Contains(err.Error(), "unsupported runtime capture provider") {
		t.Fatalf("expected unsupported provider plan error, got %v", err)
	}
}

func TestRuntimeHookResolverProductionWrappers(t *testing.T) {
	resolver := newRuntimeHookPathResolver(nil, nil)
	if _, err := resolver.runtimeNodeHookOptions(); err != nil {
		t.Fatalf("runtime node hook options with default constructor: %v", err)
	}
	if _, _, err := locateRuntimeHookPaths(); err != nil {
		t.Fatalf("locate runtime hook paths: %v", err)
	}
	if _, err := locateRuntimePythonHookDirectory(); err != nil {
		t.Fatalf("locate runtime python hook directory: %v", err)
	}
	if len(runtimeHookSearchRoots()) == 0 {
		t.Fatalf("expected runtime hook search roots from production wrapper")
	}
}

func assertEnvEntryValue(t *testing.T, env []string, key, want string) {
	t.Helper()

	got, ok := lookupEnvEntry(env, key)
	if !ok {
		t.Fatalf("expected %s to be set", key)
	}
	if got != want {
		t.Fatalf("expected %s=%q, got %q", key, want, got)
	}
}

func assertRuntimeHookStagePathNeutralError(t *testing.T, err error, requiredSubstrings ...string) {
	t.Helper()

	if err == nil {
		t.Fatal("expected runtime hook staging error")
	}
	for _, part := range requiredSubstrings {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("expected error %q to contain %q", err, part)
		}
	}
	tempRoot := os.TempDir()
	if strings.Contains(err.Error(), tempRoot) {
		t.Fatalf("expected runtime hook staging error to exclude temp root %q, got %q", tempRoot, err)
	}
	if strings.Contains(err.Error(), runtimePythonHookRelPath) {
		t.Fatalf("expected runtime hook staging error to redact hook path, got %q", err)
	}
}

func assertNodeOptionsEntry(t *testing.T, env []string, requirePath, loaderPath string) {
	t.Helper()

	nodeOptions, ok := lookupEnvEntry(env, "NODE_OPTIONS")
	if !ok {
		t.Fatalf("expected NODE_OPTIONS to be set")
	}
	if !strings.Contains(nodeOptions, "--max-old-space-size=4096") {
		t.Fatalf("expected existing NODE_OPTIONS to be preserved: %q", nodeOptions)
	}
	if strings.Contains(nodeOptions, "./scripts/runtime/") {
		t.Fatalf("expected absolute runtime hook paths, got %q", nodeOptions)
	}
	if !strings.Contains(nodeOptions, "--require="+requirePath) {
		t.Fatalf("expected require hook to be included: %q", nodeOptions)
	}
	if !strings.Contains(nodeOptions, "--loader="+loaderPath) {
		t.Fatalf("expected loader hook to be included: %q", nodeOptions)
	}
}

func lookupEnvEntry(env []string, key string) (string, bool) {
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 || parts[0] != key {
			continue
		}
		return parts[1], true
	}
	return "", false
}

func TestTrustedSearchDirs(t *testing.T) {
	secureA := t.TempDir()
	secureB := t.TempDir()
	insecure := filepath.Join(t.TempDir(), "insecure")
	if err := os.MkdirAll(insecure, 0o700); err != nil {
		t.Fatalf("mkdir insecure: %v", err)
	}
	info, err := os.Stat(insecure)
	if err != nil {
		t.Fatalf("stat insecure: %v", err)
	}
	insecurePerms := info.Mode().Perm() | 0o020
	if err := os.Chmod(insecure, insecurePerms); err != nil {
		t.Fatalf("chmod insecure: %v", err)
	}

	dirEntries := []string{
		"",
		".",
		secureA,
		insecure,
		secureB,
		secureA,
	}
	dirListValue := strings.Join(dirEntries, string(os.PathListSeparator))
	got := trustedSearchDirs(dirListValue)
	if len(got) != 2 {
		t.Fatalf("expected 2 trusted dirs, got %d: %v", len(got), got)
	}
	if got[0] != secureA {
		t.Fatalf("expected secureA first, got %q", got[0])
	}
	if got[1] != secureB {
		t.Fatalf("expected secureB second, got %q", got[1])
	}
}

func TestRuntimeSearchDirsDefault(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, "")
	_ = runtimeSearchDirs()
}

func TestRuntimeHookSearchRootsAreAnchored(t *testing.T) {
	executablePath := func() (string, error) { return filepath.Join("/tmp", "plant", "bin", "lopper"), nil }
	caller := func(skip int) (uintptr, string, int, bool) {
		return 0, filepath.Join("/tmp", "source", "internal", "runtime", "capture_env.go"), 0, true
	}
	resolver := newFakeRuntimeHookPathResolver(executablePath, caller)
	roots := resolver.runtimeHookSearchRoots()
	want := []string{
		filepath.Clean(filepath.Join("/tmp", "plant", "bin", "share", "lopper")),
		filepath.Clean(filepath.Join("/tmp", "plant", "bin", "..", "share", "lopper")),
		filepath.Clean(filepath.Join("/tmp", "source", "internal", "runtime", "..", "..")),
	}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("expected anchored runtime hook roots %v, got %v", want, roots)
	}
}

func TestRuntimeHookSearchRootsResolveRelativeCallerPaths(t *testing.T) {
	executablePath := func() (string, error) { return filepath.Join("/tmp", "plant", "bin", "lopper"), nil }
	caller := func(skip int) (uintptr, string, int, bool) { return 0, "capture_env.go", 0, true }
	resolver := newFakeRuntimeHookPathResolver(executablePath, caller)
	roots := resolver.runtimeHookSearchRoots()
	wantRepoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	want := []string{
		filepath.Clean(filepath.Join("/tmp", "plant", "bin", "share", "lopper")),
		filepath.Clean(filepath.Join("/tmp", "plant", "bin", "..", "share", "lopper")),
		wantRepoRoot,
	}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("expected relative runtime hook roots %v, got %v", want, roots)
	}
}

func TestMergeEnvAndReadEnvValue(t *testing.T) {
	base := []string{"A=1", "BADENTRY", "NODE_OPTIONS=--max-old-space-size=2048"}
	merged := mergeEnv(base, map[string]string{"A": "2", "B": "3"})
	if got := readEnvValue(merged, "A"); got != "2" {
		t.Fatalf("expected updated A value, got %q", got)
	}
	if got := readEnvValue(merged, "B"); got != "3" {
		t.Fatalf("expected B value, got %q", got)
	}
	if got := readEnvValue(merged, "MISSING"); got != "" {
		t.Fatalf("expected missing env value, got %q", got)
	}
}

func TestRuntimeNodeHookOptionsReturnsCachedError(t *testing.T) {
	sentinel := errors.New("boom")
	resolver := newFakeRuntimeHookPathResolver(func() (string, error) { return "", nil }, func(skip int) (uintptr, string, int, bool) { return 0, "", 0, false })
	resolver.hookPaths.err = sentinel
	resolver.hookPathsOnce.Do(func() {})

	_, err := resolver.runtimeNodeHookOptions()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected cached runtime hook error, got %v", err)
	}
}

func TestWithPythonRuntimeTraceEnvForResolverPropagatesError(t *testing.T) {
	sentinel := errors.New("python hook lookup failed")
	resolver := newFakeRuntimeHookPathResolver(func() (string, error) { return "", nil }, func(skip int) (uintptr, string, int, bool) { return 0, "", 0, false })
	resolver.pythonHookDir.err = sentinel
	resolver.pythonHookDirOnce.Do(func() {})

	_, err := withPythonRuntimeTraceEnvForResolver([]string{"PATH=/usr/bin"}, "/tmp/python-runtime.ndjson", "/repo", resolver)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected python hook resolver error %v, got %v", sentinel, err)
	}
}

func TestRuntimeNodeHookOptionsQuotesPathsWithSpaces(t *testing.T) {
	resolver := newFakeRuntimeHookPathResolver(func() (string, error) { return "", nil }, func(skip int) (uintptr, string, int, bool) { return 0, "", 0, false })
	resolver.hookPaths.requirePath = "/tmp/hooks/require hook.cjs"
	resolver.hookPaths.loaderPath = "/tmp/hooks/loader hook.mjs"
	resolver.hookPathsOnce.Do(func() {})

	got, err := resolver.runtimeNodeHookOptions()
	if err != nil {
		t.Fatalf("runtime node hook options: %v", err)
	}
	if !strings.Contains(got, `--require="/tmp/hooks/require hook.cjs"`) {
		t.Fatalf("expected quoted require path, got %q", got)
	}
	if !strings.Contains(got, `--loader="/tmp/hooks/loader hook.mjs"`) {
		t.Fatalf("expected quoted loader path, got %q", got)
	}
}

func TestRuntimeHookPathResolverCachesHookLookupsPerInstance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	shareRoot := filepath.Join(root, "share", "lopper")
	requirePath := filepath.Join(shareRoot, runtimeRequireHookRelPath)
	loaderPath := filepath.Join(shareRoot, runtimeLoaderHookRelPath)
	if err := os.MkdirAll(filepath.Dir(requirePath), 0o750); err != nil {
		t.Fatalf("mkdir hook dir: %v", err)
	}
	if err := os.WriteFile(requirePath, []byte("module.exports = {};\n"), 0o600); err != nil {
		t.Fatalf("write require hook: %v", err)
	}
	if err := os.WriteFile(loaderPath, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write loader hook: %v", err)
	}

	var executableCalls atomic.Int32
	executablePath := func() (string, error) {
		executableCalls.Add(1)
		return filepath.Join(root, "bin", "lopper"), nil
	}
	caller := func(skip int) (uintptr, string, int, bool) { return 0, "", 0, false }
	resolver := newFakeRuntimeHookPathResolver(executablePath, caller)

	for i := 0; i < 3; i++ {
		gotRequire, gotLoader, err := resolver.runtimeHookPaths()
		if err != nil {
			t.Fatalf("runtime hook paths call %d: %v", i+1, err)
		}
		if gotRequire != requirePath || gotLoader != loaderPath {
			t.Fatalf("unexpected cached hook paths on call %d: %q %q", i+1, gotRequire, gotLoader)
		}
	}
	if executableCalls.Load() != 1 {
		t.Fatalf("expected per-instance hook lookup cache to resolve once, got %d calls", executableCalls.Load())
	}
}

func newFakeRuntimeHookPathResolver(executablePath runtimeExecutablePathFunc, caller runtimeCallerFunc) *runtimeHookPathResolver {
	return newRuntimeHookPathResolver(executablePath, caller)
}

func TestQuoteNodeOptionPath(t *testing.T) {
	testCases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain path", in: "/tmp/hook.cjs", want: "/tmp/hook.cjs"},
		{name: "spaces", in: "/tmp/with space/hook.cjs", want: `"/tmp/with space/hook.cjs"`},
		{name: "quotes", in: `/tmp/with"quote"/hook.cjs`, want: `"/tmp/with\"quote\"/hook.cjs"`},
		{name: "windows slashes", in: `C:\Program Files\lopper\hook.cjs`, want: `"C:\\Program Files\\lopper\\hook.cjs"`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quoteNodeOptionPath(tc.in); got != tc.want {
				t.Fatalf("quote node option path: expected %q, got %q", tc.want, got)
			}
		})
	}
}
