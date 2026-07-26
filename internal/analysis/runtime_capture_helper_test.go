package analysis

import (
	"fmt"
	"net"
	"os"
	"os/exec"

	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	analysisRuntimeHelperModeEnv = "LOPPER_ANALYSIS_RUNTIME_HELPER_MODE"
	analysisRuntimeReadyAddrEnv  = "LOPPER_RUNTIME_READY_ADDR"
)

func runAnalysisRuntimeToolHelper() bool {
	mode := os.Getenv(analysisRuntimeHelperModeEnv)
	if mode == "" {
		return false
	}

	switch mode {
	case "count-trace":
		runAnalysisRuntimeCounterTraceHelper()
	case "count-only":
		runAnalysisRuntimeCounterOnlyHelper()
	case "ready-block":
		runAnalysisRuntimeReadyBlockHelper()
	case "python-import-requests":
		runAnalysisRuntimePythonImportRequestsHelper()
	default:
		fmt.Fprintf(os.Stderr, "unknown runtime helper mode %q\n", mode)
		os.Exit(2)
	}
	return true
}

func runAnalysisRuntimeCounterTraceHelper() {
	testutil.MustIncrementRuntimeHelperCounterFromEnv()
	tracePath := os.Getenv("LOPPER_RUNTIME_TRACE")
	if tracePath != "" {
		testutil.MustWriteRuntimeHelperFile(tracePath, "{\"module\":\"lodash/map\"}\n")
	}
	os.Exit(0)
}

func runAnalysisRuntimeCounterOnlyHelper() {
	testutil.MustIncrementRuntimeHelperCounterFromEnv()
	os.Exit(0)
}

func runAnalysisRuntimeReadyBlockHelper() {
	readyAddr := os.Getenv(analysisRuntimeReadyAddrEnv)
	if readyAddr == "" {
		fmt.Fprintln(os.Stderr, "missing readiness address")
		os.Exit(2)
	}
	conn, err := net.Dial("tcp", readyAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := conn.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	select {}
}

func runAnalysisRuntimePythonImportRequestsHelper() {
	pythonPath := os.Getenv("LOPPER_TEST_PYTHON")
	if pythonPath == "" {
		fmt.Fprintln(os.Stderr, "missing test python path")
		os.Exit(2)
	}
	cmd := exec.Command(pythonPath, "-c", "import requests")
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
