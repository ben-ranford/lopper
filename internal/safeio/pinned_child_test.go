package safeio

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

func TestOpenPinnedChildRootPreservesOwnershipAndCleanup(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lookupErr := errors.New("opened child lookup failed")
	closeErr := errors.New("child close failed")
	for _, failing := range []bool{false, true} {
		closed := 0
		child := &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "." {
					t.Fatalf("child lookup = %q", name)
				}
				if failing {
					return nil, lookupErr
				}
				return info, nil
			},
			close: func() error { closed++; return closeErr },
		}
		parent := &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "child" {
					t.Fatalf("parent lookup = %q", name)
				}
				return info, nil
			},
			openRoot: func(name string) (Root, error) {
				if name != "child" {
					t.Fatalf("open child = %q", name)
				}
				return child, nil
			},
		}
		got, openErr := OpenPinnedChildRoot[Root](parent, "child", "parent/child", "root changed while opening")
		if failing {
			if got != nil || !errors.Is(openErr, lookupErr) || !errors.Is(openErr, closeErr) || closed != 1 {
				t.Fatalf("failed child = %v, error = %v, closes = %d", got, openErr, closed)
			}
		} else if got != child || openErr != nil || closed != 0 {
			t.Fatalf("successful child = %v, error = %v, closes = %d", got, openErr, closed)
		}
	}
}
