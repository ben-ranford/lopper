//go:build !darwin && !linux

package safeio

import (
	"fmt"
	"os"
	"runtime"
)

func OpenCanonicalSearchOnlyWriteRoot(string) (*WriteRoot, error) {
	return nil, fmt.Errorf("%w on %s", ErrSearchOnlyWriteRootUnsupported, runtime.GOOS)
}

func WriteFileAtomicallyIfAbsentUnderCanonicalPath(string, []byte, os.FileMode) error {
	return fmt.Errorf("%w on %s", ErrSearchOnlyWriteRootUnsupported, runtime.GOOS)
}

func WriteFileAtomicallyReplacingUnderCanonicalPath(string, []byte, os.FileMode) error {
	return fmt.Errorf("%w on %s", ErrSearchOnlyWriteRootUnsupported, runtime.GOOS)
}

func (*WriteRoot) WriteFileAtomicallyIfAbsentUnderPinnedRoot(string, []byte, os.FileMode) error {
	return fmt.Errorf("%w on %s", ErrSearchOnlyWriteRootUnsupported, runtime.GOOS)
}

func (*WriteRoot) WriteFileAtomicallyReplacingUnderPinnedRoot(string, []byte, os.FileMode) error {
	return fmt.Errorf("%w on %s", ErrSearchOnlyWriteRootUnsupported, runtime.GOOS)
}

func openSearchOnlyDirectory(string) (*os.File, error) {
	return nil, fmt.Errorf("%w on %s", ErrSearchOnlyWriteRootUnsupported, runtime.GOOS)
}
