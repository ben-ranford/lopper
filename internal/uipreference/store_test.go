package uipreference

import (
	"errors"
	"github.com/ben-ranford/lopper/internal/safeio"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestStoreLifecycleAndPermissions(t *testing.T) {
	root := t.TempDir()
	s := Store{ConfigDir: func() (string, error) { return root, nil }}
	if got, err := s.Load(); err != nil || got != "" {
		t.Fatalf("load=%q %v", got, err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	for _, choice := range []string{Stave, Legacy} {
		if err := s.Save(choice); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Load(); err != nil || got != choice {
			t.Fatalf("load=%q %v", got, err)
		}
	}
	path, err := s.path()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v", info.Mode())
	}
	info, err = os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode=%v", info.Mode())
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(); err != nil || got != "" {
		t.Fatalf("load=%q %v", got, err)
	}
}

func TestStoreCorruptionAndRecovery(t *testing.T) {
	s := Store{ConfigDir: func() (string, error) { return t.TempDir(), nil }}
	root := t.TempDir()
	s.ConfigDir = func() (string, error) { return root, nil }
	if err := s.Save(Stave); err != nil {
		t.Fatal(err)
	}
	path, err := s.path()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"{", `{"version":2,"ui":"stave"}`, `{"version":1,"ui":"unknown"}`, `{}`} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Load(); err == nil {
			t.Fatal("accepted corrupt preference")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != bad {
			t.Fatalf("modified corruption %q %v", got, err)
		}
		if err := s.Save(Legacy); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(Ask); err == nil {
		t.Fatal("accepted ask for save")
	}
}

func TestStoreConcurrentSavesNeverPublishPartialJSON(t *testing.T) {
	root := t.TempDir()
	s := Store{ConfigDir: func() (string, error) { return root, nil }}
	if err := s.Save(Legacy); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				if err := s.Save(Stave); err != nil {
					t.Error(err)
				}
				if got, err := s.Load(); err != nil || (got != Stave && got != Legacy) {
					t.Errorf("partial preference %q %v", got, err)
				}
			}
		}()
	}
	wg.Wait()
	if err := s.Save(Legacy); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(); err != nil || got != Legacy {
		t.Fatalf("last successful write=%q %v", got, err)
	}
}

func TestStoreFilesystemErrors(t *testing.T) {
	failure := errors.New("no config directory")
	s := Store{ConfigDir: func() (string, error) { return "", failure }}
	if _, err := s.Load(); !errors.Is(err, failure) {
		t.Fatalf("load=%v", err)
	}
	if err := s.Save(Stave); !errors.Is(err, failure) {
		t.Fatalf("save=%v", err)
	}
	if err := s.Clear(); !errors.Is(err, failure) {
		t.Fatalf("clear=%v", err)
	}
	root := t.TempDir()
	s.ConfigDir = func() (string, error) { return root, nil }
	path, err := s.path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("read directory")
	}
	if err := s.Save(Stave); err == nil {
		t.Fatal("overwrote directory")
	}
	if err := s.Clear(); err == nil {
		t.Fatal("removed occupied directory")
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "lopper"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.ConfigDir = func() (string, error) { return other, nil }
	if err := s.Save(Stave); err == nil {
		t.Fatal("created directory over file")
	}
}

func TestStoreUsesPlatformUserDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("AppData", root)
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path, err := (&Store{}).path()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "lopper", "ui-preferences.json") {
		t.Fatalf("path=%q", path)
	}
}

func TestStoreRestrictsExistingPermissions(t *testing.T) {
	root := t.TempDir()
	s := Store{ConfigDir: func() (string, error) { return root, nil }}
	if err := s.Save(Stave); err != nil {
		t.Fatal(err)
	}
	path, err := s.path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(Legacy); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("retained permissive mode %v", info.Mode())
	}
	if got, err := s.Load(); err != nil || got != Legacy {
		t.Fatalf("load=%q %v", got, err)
	}
}

type preferenceFailRoot struct {
	safeio.Root
	failure string
}

func (r *preferenceFailRoot) OpenFile(name string, flags int, mode os.FileMode) (safeio.File, error) {
	if r.failure == "create" {
		return nil, io.ErrClosedPipe
	}
	file, err := r.Root.OpenFile(name, flags, mode)
	if err != nil {
		return nil, err
	}
	return &preferenceFailFile{File: file, failure: r.failure}, nil
}
func (r *preferenceFailRoot) Rename(oldName, newName string) error {
	if r.failure == "rename" {
		return io.ErrClosedPipe
	}
	return r.Root.Rename(oldName, newName)
}

type preferenceFailFile struct {
	safeio.File
	failure string
}

func (f *preferenceFailFile) Write(data []byte) (int, error) {
	if f.failure == "write" {
		return 0, io.ErrClosedPipe
	}
	if f.failure == "short" {
		return 0, nil
	}
	return f.File.Write(data)
}
func (f *preferenceFailFile) Close() error {
	err := f.File.Close()
	if f.failure == "close" && err == nil {
		return io.ErrClosedPipe
	}
	return err
}
func TestPreferencePublishFailuresPreserveExistingFile(t *testing.T) {
	for _, failure := range []string{"create", "write", "short", "close", "rename"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "ui-preferences.json")
			if err := os.WriteFile(path, []byte("previous"), 0o600); err != nil {
				t.Fatal(err)
			}
			root, err := safeio.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := root.Close(); err != nil {
					t.Error(err)
				}
			}()
			if err := publishPreference(&preferenceFailRoot{Root: root, failure: failure}, "ui-preferences.json", []byte("new")); err == nil {
				t.Fatal("failure ignored")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "previous" {
				t.Fatalf("previous modified=%q %v", data, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary file leak: %v %v", entries, err)
			}
		})
	}
	if err := writePreference(filepath.Join(t.TempDir(), "missing"), "ui-preferences.json", nil); err == nil {
		t.Fatal("missing parent accepted")
	}
}
