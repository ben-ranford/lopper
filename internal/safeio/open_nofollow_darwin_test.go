//go:build darwin

package safeio

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenFileNoFollowSupportsVerifiedOpenOnDarwin(t *testing.T) {
	if !OpenFileNoFollowSupported() {
		t.Fatal("expected darwin no-follow support to be enabled")
	}

	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	const want = "{\"module\":\"lodash/map\"}\n"
	if err := os.WriteFile(tracePath, []byte(want), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	file, err := OpenFileNoFollow(tracePath)
	if err != nil {
		t.Fatalf("OpenFileNoFollow(%q): %v", tracePath, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close file: %v", closeErr)
		}
	}()

	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	if string(data) != want {
		t.Fatalf("unexpected trace data: got %q want %q", string(data), want)
	}
}

func TestOpenParentRootNoFollowDarwinAliasJoinsLookupAndCloseErrors(t *testing.T) {
	lookupErr := errors.New("alias lookup failed")
	closeErr := errors.New("close volume root failed")
	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		if name != string(filepath.Separator) {
			t.Fatalf("unexpected volume root: %q", name)
		}
		return &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "private" {
					t.Fatalf("unexpected alias component: %q", name)
				}
				return nil, lookupErr
			},
			close: func() error { return closeErr },
		}, nil
	}})

	root, err := openParentRootNoFollow(filepath.Join(string(filepath.Separator), "tmp", "trace-parent"))
	if root != nil {
		t.Fatal("expected failed alias traversal to return no root")
	}
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected alias lookup error, got %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected volume root close error, got %v", err)
	}
}

func TestOpenParentRootNoFollowDarwinAliasClosesChildAfterParentCloseError(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	parentCloseErr := errors.New("close parent alias root failed")
	childCloseErr := errors.New("close child alias root failed")
	childClosed := false

	child := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "." {
				t.Fatalf("unexpected child lstat path: %q", name)
			}
			return dirInfo, nil
		},
		close: func() error {
			childClosed = true
			return childCloseErr
		},
	}
	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		if name != string(filepath.Separator) {
			t.Fatalf("unexpected volume root: %q", name)
		}
		return &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "private" {
					t.Fatalf("unexpected alias component: %q", name)
				}
				return dirInfo, nil
			},
			openRoot: func(name string) (Root, error) {
				if name != "private" {
					t.Fatalf("unexpected opened alias component: %q", name)
				}
				return child, nil
			},
			close: func() error { return parentCloseErr },
		}, nil
	}})

	root, err := openParentRootNoFollow(filepath.Join(string(filepath.Separator), "tmp", "trace-parent"))
	if root != nil {
		t.Fatal("expected parent close failure to return no root")
	}
	if !errors.Is(err, parentCloseErr) {
		t.Fatalf("expected parent close error, got %v", err)
	}
	if !errors.Is(err, childCloseErr) {
		t.Fatalf("expected child close error, got %v", err)
	}
	if !childClosed {
		t.Fatal("expected child alias root to be closed")
	}
}
