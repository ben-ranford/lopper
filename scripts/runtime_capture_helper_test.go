package scripts

import (
	"os"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
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

	testutil.MustIncrementRuntimeHelperCounterFromEnv()
	tracePath := os.Getenv("LOPPER_RUNTIME_TRACE")
	if tracePath != "" {
		testutil.MustWriteRuntimeHelperFile(tracePath, "{\"module\":\"lodash/map\"}\n")
	}
	os.Exit(0)
	return true
}
