package safeio

import (
	"errors"
	"io/fs"
	"os"
	"testing"
)

func TestOpenPinnedChildRootPreservesOwnershipAndCleanup(t *testing.T) {
	t.Run("successful ownership transfer", func(t *testing.T) { testPinnedChildOwnership(t, false) })
	t.Run("failed lookup closes child", func(t *testing.T) { testPinnedChildOwnership(t, true) })
}

func testPinnedChildOwnership(t *testing.T, failing bool) {
	t.Helper()
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lookupErr := errors.New("opened child lookup failed")
	closeErr := errors.New("child close failed")
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
	parent := newPinnedParentFixture(t, "child", info, child)
	got, openErr := OpenPinnedChildRoot[Root](parent, "child", "parent/child", "root changed while opening")
	if failing {
		if got != nil || !errors.Is(openErr, lookupErr) || !errors.Is(openErr, closeErr) || closed != 1 {
			t.Fatalf("failed child = %v, error = %v, closes = %d", got, openErr, closed)
		}
	} else if got != child || openErr != nil || closed != 0 {
		t.Fatalf("successful child = %v, error = %v, closes = %d", got, openErr, closed)
	}
}

// newPinnedParentFixture verifies that traversal looks up and opens exactly the
// expected entry before returning the caller's child handle.
func newPinnedParentFixture(t *testing.T, childName string, info fs.FileInfo, child Root) *fakeRoot {
	t.Helper()
	return &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != childName {
				t.Fatalf("unexpected parent lstat %q, want %q", name, childName)
			}
			return info, nil
		},
		openRoot: func(name string) (Root, error) {
			if name != childName {
				t.Fatalf("unexpected parent openRoot %q, want %q", name, childName)
			}
			return child, nil
		},
	}
}
