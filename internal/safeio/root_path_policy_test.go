package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestExistingRootAncestorOpenFailureDoesNotBecomeMissingSuffix(t *testing.T) {
	closeErr := errors.New("close root failed")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, nil },
		close: func() error { return closeErr },
	}
	name := filepath.Join(string(os.PathSeparator), "parent", "child")
	opened, ancestor, missing, err := openRootPathWith(name,
		func(string) (string, error) { return name, nil },
		filepath.Rel,
		func(string) (Root, error) { return root, nil },
		func(Root, string, string) (Root, string, error) { return nil, "", os.ErrNotExist },
		allowMissingRootSuffix,
	)
	if opened != nil || ancestor != "" || len(missing) != 0 {
		t.Fatalf("child-open failure returned ancestor state: %v %q %v", opened, ancestor, missing)
	}
	if !errors.Is(err, os.ErrNotExist) || !errors.Is(err, closeErr) {
		t.Fatalf("expected child-open and close errors, got %v", err)
	}
}
