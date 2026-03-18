package testsupport

import (
	"os"
	"testing"

	"go.uber.org/goleak"
)

func RunOptionalLeakMain(m *testing.M, options ...goleak.Option) {
	if os.Getenv("GOLEAK") == "" {
		os.Exit(m.Run())
	}
	goleak.VerifyTestMain(m, options...)
}
