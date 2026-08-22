//go:build !darwin && !linux

package safeio

import (
	"errors"
	"os"
	"testing"
)

func TestSearchOnlyWriteRootUnsupportedErrorsAreSentinel(t *testing.T) {
	writers := []func() error{
		func() error {
			_, err := OpenCanonicalSearchOnlyWriteRoot(t.TempDir())
			return err
		},
		func() error {
			return WriteFileAtomicallyIfAbsentUnderCanonicalPath("profile.yaml", []byte("after"), 0o600)
		},
		func() error {
			return WriteFileAtomicallyReplacingUnderCanonicalPath("profile.yaml", []byte("after"), 0o600)
		},
		func() error {
			return (*WriteRoot)(nil).WriteFileAtomicallyIfAbsentUnderPinnedRoot("profile.yaml", []byte("after"), 0o600)
		},
		func() error {
			return (*WriteRoot)(nil).WriteFileAtomicallyReplacingUnderPinnedRoot("profile.yaml", []byte("after"), 0o600)
		},
		func() error {
			file, err := openSearchOnlyDirectory(t.TempDir())
			if file != nil {
				t.Fatalf("expected unsupported open to return no file")
			}
			return err
		},
	}

	for _, writer := range writers {
		if err := writer(); !errors.Is(err, ErrSearchOnlyWriteRootUnsupported) {
			t.Fatalf("expected unsupported sentinel, got %v", err)
		}
	}
}

func TestSearchOnlyUnsupportedSentinelDoesNotMatchPermission(t *testing.T) {
	if errors.Is(ErrSearchOnlyWriteRootUnsupported, os.ErrPermission) {
		t.Fatal("unsupported search-only fallback must not masquerade as permission")
	}
}
