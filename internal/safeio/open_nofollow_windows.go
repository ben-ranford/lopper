//go:build windows

package safeio

import (
	"fmt"
	"os"
)

func openRootFileNoFollow(_ *os.Root, _ string) (*os.File, error) {
	return nil, fmt.Errorf("no-follow file open unsupported on windows: fail closed rather than follow a final-component reparse point")
}
