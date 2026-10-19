//go:build lopper_native_windows_unc

package runtime

import (
	"os"
	"strings"
	"testing"
)

func TestTrustedSearchDirsWindowsUNCPaths(t *testing.T) {
	share := os.Getenv("LOPPER_WINDOWS_TEST_UNC_DIR")
	parts := strings.Split(strings.TrimPrefix(share, `\\`), `\`)
	if !strings.HasPrefix(share, `\\`) || len(parts) < 2 || parts[0] == "" || parts[1] == "" || parts[0] == "?" || parts[0] == "." {
		t.Fatal("native UNC fixture requires LOPPER_WINDOWS_TEST_UNC_DIR with an accessible share")
	}
	dir, err := os.MkdirTemp(share, "lopper-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Error(err)
		}
	})
	assertWindowsRuntimeSearchPaths(t, setupWindowsRuntimeSearchExecutable(t, dir))
}
