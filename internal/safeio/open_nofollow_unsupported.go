//go:build !(unix || windows)

package safeio

import (
	"fmt"
	"os"
	"runtime"
)

func openRootFileNoFollow(*os.Root, string) (*os.File, error) {
	return nil, fmt.Errorf("no-follow file open unsupported on %s: cannot prove a pinned readable regular-file handle", runtime.GOOS)
}

func runtimeGOOS() string {
	return runtime.GOOS
}
