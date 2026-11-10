package main

import (
	"strings"

	"os"
	"path/filepath"
	"testing"
)

func TestCachedPythonMissHasNoProvisioningSideEffect(t *testing.T) {
	root := providerTestRoot(t)
	if _, err := cachedPython(root, root); err == nil {
		t.Fatal("cache miss admitted")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("cache miss created files")
	}
	runtime := filepath.Join(root, "Python", "3.13.16", "x64")
	if err := os.MkdirAll(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime, "python.exe"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedPython(root, root); err == nil {
		t.Fatal("incomplete cached runtime admitted")
	}
	if err := os.WriteFile(runtime+".complete", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := cachedPython(root, root); err != nil || got != runtime {
		t.Fatalf("completed cache not found: %q %v", got, err)
	}
	if _, err := cachedPython(root, filepath.Join(root, "other")); err == nil {
		t.Fatal("ambient alternate tool cache admitted")
	}
}

func TestCachedPythonRejectsMissingRootsWithoutWriting(t *testing.T) {
	root := providerTestRoot(t)
	for _, runner := range []string{"", filepath.Join(root, "missing"), "relative", root + string(os.PathSeparator) + ".."} {
		if _, err := cachedPython(runner, runner); err == nil {
			t.Fatalf("invalid cache admitted: %q", runner)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed cache check created entries: %v", entries)
	}
}

func TestCachedPythonRequiresRegularInterpreterAfterCompletion(t *testing.T) {
	root := providerTestRoot(t)
	runtime := filepath.Join(root, "Python", "3.13.16", "x64")
	if err := os.MkdirAll(runtime, 0700); err != nil {
		t.Fatal(err)
	}
	marker := runtime + ".complete"
	if err := os.WriteFile(marker, []byte("completed"), 0600); err != nil {
		t.Fatal(err)
	}
	assertCachedInterpreterMissing(t, root)
	if err := os.Mkdir(filepath.Join(runtime, "python.exe"), 0700); err != nil {
		t.Fatal(err)
	}
	assertCachedInterpreterMissing(t, root)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "completed" {
		t.Fatal("cache check changed completion marker")
	}
	entries, err := os.ReadDir(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatal("cache check changed interpreter fixture")
	}
}

func assertCachedInterpreterMissing(t *testing.T, root string) {
	t.Helper()
	if _, err := cachedPython(root, root); err == nil || !strings.Contains(err.Error(), "interpreter missing") {
		t.Fatalf("invalid interpreter admitted: %v", err)
	}
}
