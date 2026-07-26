package runtime

import (
	"sync"
	"testing"
)

func restoreRuntimeHookState(t *testing.T) {
	t.Helper()

	originalRequire := runtimeRequireHookPath
	originalLoader := runtimeLoaderHookPath
	originalErr := runtimeHookPathsErr
	originalPythonDir := runtimePythonHookDirPath
	originalPythonErr := runtimePythonHookDirErr

	t.Cleanup(func() {
		runtimeHookPathsOnce = sync.Once{}
		runtimeHookPathsOnce.Do(func() {
			runtimeRequireHookPath = originalRequire
			runtimeLoaderHookPath = originalLoader
			runtimeHookPathsErr = originalErr
		})
		runtimePythonHookDirOnce = sync.Once{}
		runtimePythonHookDirOnce.Do(func() {
			runtimePythonHookDirPath = originalPythonDir
			runtimePythonHookDirErr = originalPythonErr
		})
	})
}
