//go:build windows

package advisory

import "testing"

func TestDownloadSnapshotWriteError(t *testing.T) {
	t.Skip("RLIMIT_FSIZE is unavailable on Windows")
}
