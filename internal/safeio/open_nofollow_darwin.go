//go:build darwin

package safeio

import (
	"fmt"
	"os"
)

func openRootFileNoFollow(*os.Root, string) (*os.File, error) {
	return nil, fmt.Errorf("no-follow file open unsupported on darwin: cannot prove a pinned readable regular-file handle without reopening by path")
}
