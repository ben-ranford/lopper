package analysis

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/testsupport"
)

func TestMain(m *testing.M) {
	if runAnalysisRuntimeToolHelper() {
		return
	}
	testsupport.RunOptionalLeakMain(m)
}
