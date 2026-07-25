package runtime

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
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
	restore := stubLoadRuntimeTraceFile(func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	})
	t.Cleanup(restore)
	return Load("runtime.ndjson")
}

func loadTraceFromContentContext(ctx context.Context, t *testing.T, content string) (Trace, error) {
	t.Helper()
	restore := stubLoadRuntimeTraceFile(func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	})
	t.Cleanup(restore)
	return LoadContext(ctx, "runtime.ndjson")
}

func requireRuntimeTracePathOpenSupport(t *testing.T) {
	t.Helper()
	if !safeio.OpenFileNoFollowSupported() {
		t.Skip("runtime trace path opening is fail-closed on this platform")
	}
}
