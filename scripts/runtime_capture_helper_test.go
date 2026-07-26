package scripts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const scriptsRuntimeHelperModeEnv = "LOPPER_SCRIPTS_RUNTIME_HELPER_MODE"

func TestMain(m *testing.M) {
	if runScriptsRuntimeToolHelper() {
		return
	}
	os.Exit(m.Run())
}

func runScriptsRuntimeToolHelper() bool {
	if os.Getenv(scriptsRuntimeHelperModeEnv) == "" {
		return false
	}

	mustIncrementScriptsRuntimeHelperCounterFromEnv()
	tracePath := os.Getenv("LOPPER_RUNTIME_TRACE")
	if tracePath != "" {
		mustWriteScriptsRuntimeHelperFile(tracePath, "{\"module\":\"lodash/map\"}\n")
	}
	os.Exit(0)
	return true
}

func mustWriteScriptsRuntimeHelperFile(path string, content string) {
	parentDir := filepath.Dir(path)
	if err := os.MkdirAll(parentDir, 0o750); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root, err := os.OpenRoot(parentDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	file, err := root.OpenFile(filepath.Base(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		closeErr := root.Close()
		fmt.Fprintln(os.Stderr, errors.Join(err, closeErr))
		os.Exit(1)
	}
	if _, err := file.WriteString(content); err != nil {
		fileCloseErr := file.Close()
		rootCloseErr := root.Close()
		fmt.Fprintln(os.Stderr, errors.Join(err, fileCloseErr, rootCloseErr))
		os.Exit(1)
	}
	if err := file.Close(); err != nil {
		closeErr := root.Close()
		fmt.Fprintln(os.Stderr, errors.Join(err, closeErr))
		os.Exit(1)
	}
	if err := root.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func mustIncrementScriptsRuntimeHelperCounterFromEnv() {
	counterPath := os.Getenv("LOPPER_RUNTIME_COUNTER")
	if counterPath == "" {
		fmt.Fprintln(os.Stderr, "missing runtime counter path")
		os.Exit(2)
	}

	count := 1
	if content, err := os.ReadFile(counterPath); err == nil {
		var parsed int
		if _, scanErr := fmt.Sscanf(string(content), "%d", &parsed); scanErr == nil {
			count = parsed + 1
		}
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mustWriteScriptsRuntimeHelperFile(counterPath, fmt.Sprintf("%d", count))
}
