//go:build unix && !linux && !darwin

package safeio

import (
	"os"
	"runtime"
)

func openRootFileNoFollow(*os.Root, string) (*os.File, error) {
	return nil, openFileNoFollowUnsupportedError(runtime.GOOS)
}
