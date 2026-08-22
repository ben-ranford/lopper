//go:build !darwin && !linux

package safeio

import (
	"fmt"
	"os"
	"runtime"
)

func WriteFileAtomicallyIfAbsentUnderCanonicalPath(string, []byte, os.FileMode) error {
	return fmt.Errorf("search-only write root is not supported on %s", runtime.GOOS)
}

func WriteFileAtomicallyReplacingUnderCanonicalPath(string, []byte, os.FileMode) error {
	return fmt.Errorf("search-only write root is not supported on %s", runtime.GOOS)
}

func (*WriteRoot) WriteFileAtomicallyIfAbsentUnderPinnedRoot(string, []byte, os.FileMode) error {
	return fmt.Errorf("search-only write root is not supported on %s", runtime.GOOS)
}

func (*WriteRoot) WriteFileAtomicallyReplacingUnderPinnedRoot(string, []byte, os.FileMode) error {
	return fmt.Errorf("search-only write root is not supported on %s", runtime.GOOS)
}

func openSearchOnlyDirectory(string) (*os.File, error) {
	return nil, fmt.Errorf("search-only write root is not supported on %s", runtime.GOOS)
}
