//go:build !(unix || windows)

package safeio

import (
	"os"
	"runtime"
)

func openRootFileNoFollow(*os.Root, string) (*os.File, error) {
	return nil, openFileNoFollowUnsupportedError(runtime.GOOS)
}

func runtimeGOOS() string {
	return runtime.GOOS
}
