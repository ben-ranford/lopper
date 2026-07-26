package advisory

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestLoadCacheManifestRejectsInvalidJSON(t *testing.T) {
	cachePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cachePath, manifestFileName), []byte(`{"schemaVersion":`), 0o600); err != nil {
		t.Fatalf("write invalid advisory manifest: %v", err)
	}

	manifest, err := LoadCacheManifest(cachePath)
	if manifest.SchemaVersion != "" || manifest.Latest != "" || len(manifest.Snapshots) != 0 {
		t.Fatalf("expected invalid manifest to return no parsed state, got %#v", manifest)
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("expected JSON syntax error identity, got %v", err)
	}
	if !strings.Contains(err.Error(), "parse advisory cache manifest") {
		t.Fatalf("expected advisory manifest parse context, got %v", err)
	}
}

func TestPrepareAdvisoryCacheRootJoinsFirstChildFailureWithAncestorClose(t *testing.T) {
	lookupErr := errors.New("lookup first advisory cache child")
	closeErr := errors.New("close advisory cache ancestor")
	cachePath := filepath.Join(string(os.PathSeparator), "cache")
	requestedPath := filepath.Join(cachePath, "leaf")
	closeCalls := 0
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "leaf" {
				t.Fatalf("unexpected advisory cache child lookup %q", name)
			}
			return nil, lookupErr
		},
		close: func() error {
			closeCalls++
			return closeErr
		},
	}
	original := openAdvisoryCacheAncestor
	openAdvisoryCacheAncestor = func(string) (safeio.Root, string, []string, error) {
		return root, cachePath, []string{"leaf"}, nil
	}
	t.Cleanup(func() {
		openAdvisoryCacheAncestor = original
	})

	opened, err := prepareAdvisoryCacheRoot(requestedPath)
	if opened != nil {
		t.Fatalf("expected first-child failure to return no cache root, got %#v", opened)
	}
	if !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected child lookup and ancestor close identities, got %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("expected failed advisory cache ancestor to close once, got %d", closeCalls)
	}
}

func TestAdvisoryOpenOrCreatePinnedChildPropagatesMkdirFailure(t *testing.T) {
	mkdirErr := errors.New("create advisory cache child")
	lstatCalls := 0
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "leaf" {
				t.Fatalf("unexpected advisory child lookup %q", name)
			}
			lstatCalls++
			return nil, os.ErrNotExist
		},
		mkdir: func(name string, perm os.FileMode) error {
			if name != "leaf" || perm != 0o750 {
				t.Fatalf("unexpected advisory child mkdir %q with mode %#o", name, perm)
			}
			return mkdirErr
		},
		openRoot: func(string) (safeio.Root, error) {
			t.Fatal("advisory child root must not open after mkdir failure")
			return nil, nil
		},
	}

	child, err := advisoryOpenOrCreatePinnedChild(root, "/cache", "leaf")
	if child != nil {
		t.Fatalf("expected mkdir failure to return no child root, got %#v", child)
	}
	if !errors.Is(err, mkdirErr) {
		t.Fatalf("expected mkdir error identity, got %v", err)
	}
	if lstatCalls != 1 {
		t.Fatalf("expected no post-mkdir lookup after failure, got %d lookups", lstatCalls)
	}
}

func TestAdvisoryOpenOrCreatePinnedChildPropagatesPostCreateLookupFailure(t *testing.T) {
	lookupErr := errors.New("lookup newly created advisory child")
	lstatCalls := 0
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "leaf" {
				t.Fatalf("unexpected advisory child lookup %q", name)
			}
			lstatCalls++
			if lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return nil, lookupErr
		},
		mkdir: func(name string, perm os.FileMode) error {
			if name != "leaf" || perm != 0o750 {
				t.Fatalf("unexpected advisory child mkdir %q with mode %#o", name, perm)
			}
			return nil
		},
		openRoot: func(string) (safeio.Root, error) {
			t.Fatal("advisory child root must not open after post-create lookup failure")
			return nil, nil
		},
	}

	child, err := advisoryOpenOrCreatePinnedChild(root, "/cache", "leaf")
	if child != nil {
		t.Fatalf("expected post-create lookup failure to return no child root, got %#v", child)
	}
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected post-create lookup error identity, got %v", err)
	}
	if lstatCalls != 2 {
		t.Fatalf("expected lookup before and after child creation, got %d", lstatCalls)
	}
}

func TestAdvisoryOpenOrCreatePinnedChildRejectsNonDirectory(t *testing.T) {
	parentPath := filepath.Join(string(os.PathSeparator), "cache")
	filePath := filepath.Join(t.TempDir(), "cache-child")
	if err := os.WriteFile(filePath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write advisory cache child fixture: %v", err)
	}
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat advisory cache child fixture: %v", err)
	}
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "leaf" {
				t.Fatalf("unexpected advisory child lookup %q", name)
			}
			return fileInfo, nil
		},
		openRoot: func(string) (safeio.Root, error) {
			t.Fatal("non-directory advisory child must not be opened as a root")
			return nil, nil
		},
	}

	child, err := advisoryOpenOrCreatePinnedChild(root, parentPath, "leaf")
	if child != nil {
		t.Fatalf("expected non-directory advisory child to be rejected, got %#v", child)
	}
	wantErr := "root is not a directory: " + filepath.Join(parentPath, "leaf")
	if err == nil || err.Error() != wantErr {
		t.Fatalf("expected exact non-directory path context, got %v", err)
	}
}

func TestAdvisoryOpenOrCreatePinnedChildPropagatesOpenRootFailure(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat advisory child directory fixture: %v", err)
	}
	openErr := errors.New("open advisory cache child root")
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "leaf" {
				t.Fatalf("unexpected advisory child lookup %q", name)
			}
			return dirInfo, nil
		},
		openRoot: func(name string) (safeio.Root, error) {
			if name != "leaf" {
				t.Fatalf("unexpected advisory child root open %q", name)
			}
			return nil, openErr
		},
	}

	child, err := advisoryOpenOrCreatePinnedChild(root, "/cache", "leaf")
	if child != nil {
		t.Fatalf("expected child root open failure to return no root, got %#v", child)
	}
	if !errors.Is(err, openErr) {
		t.Fatalf("expected child root open error identity, got %v", err)
	}
}
