//go:build windows

package advisory

import "testing"

func runDownloadSnapshotWriteErrorChild(t *testing.T) {
	t.Skip("requires RLIMIT_FSIZE")
}
