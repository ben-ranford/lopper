package runtime

import (
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	scopePkgDependency         = "@scope/pkg"
	lodashMapModule            = "lodash/map"
	expectedGotFormat          = "%s: expected %q, got %q"
	loadTraceErrFmt            = "load trace: %v"
	leftPadDependency          = "left-pad"
	leftPadModule              = "left-pad/index"
	leftPadResolvedIndexModule = "/repo/node_modules/left-pad/index.js"
	alphaIndexModule           = "alpha/index.js"
	zetaIndexModule            = "zeta/index.js"
)

func loadTraceFromContent(t *testing.T, content string) (Trace, error) {
	t.Helper()
	return Load(testutil.WriteTempFile(t, "runtime.ndjson", content))
}
