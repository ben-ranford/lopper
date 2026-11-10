//go:build windows

package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRegularFileWithinRootLimitWindowsNamespaceAndRegularControls(t *testing.T) {
	repo := t.TempDir()
	root, err := OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := os.WriteFile(filepath.Join(repo, "regular.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadRegularFileWithinRootLimit(root, "regular.txt", 2)
	if err != nil || string(data) != "ok" {
		t.Fatalf("regular rooted read data=%q error=%v", data, err)
	}
	if _, err := ReadRegularFileWithinRootLimit(root, "regular.txt", 1); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("regular byte limit not preserved: %v", err)
	}

	for _, name := range []string{`\\.\pipe\lopper-no-listener`, `\\?\C:\outside`, `\Device\NamedPipe\x`, "NUL", "NUL.txt", "COM1"} {
		if _, err := ReadRegularFileWithinRootLimit(root, name, 1); err == nil {
			t.Errorf("Windows namespace/device path %q was accepted", name)
		}
	}
}

func TestOpenPinnedRegularFileWindowsRejectsLeafReplacement(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "leaf")
	if err := os.WriteFile(path, []byte("regular"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	wrapper := &windowsRegularReplaceRoot{Root: root, path: path}
	file, err := OpenPinnedRegularFile(wrapper, "leaf")
	if err == nil {
		_ = file.Close()
		t.Fatal("regular-observed directory replacement was accepted")
	}
	if !wrapper.replaced {
		t.Fatal("leaf was not replaced at the rooted open seam")
	}
}

type windowsRegularReplaceRoot struct {
	Root
	path     string
	replaced bool
}

func (r *windowsRegularReplaceRoot) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	if name == "leaf" && !r.replaced {
		r.replaced = true
		if err := os.Remove(r.path); err != nil {
			return nil, err
		}
		if err := os.Mkdir(r.path, 0o700); err != nil {
			return nil, err
		}
	}
	return r.Root.OpenFile(name, flag, perm)
}
