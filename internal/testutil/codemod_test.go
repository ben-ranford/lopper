package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteLodashMapFixture(t *testing.T) {
	repo := t.TempDir()
	source := "import { map } from \"lodash\";\r\n"
	path := WriteLodashMapFixture(t, repo, source)
	// Use the same creation mode so the reference observes the process umask.
	referencePath := filepath.Join(repo, "reference.js")
	if err := os.WriteFile(referencePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	referenceInfo, err := os.Stat(referencePath)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(repo, "index.js") {
		t.Fatalf("source path = %q", path)
	}
	for name, want := range map[string]string{
		"index.js":                         source,
		"node_modules/lodash/package.json": "{\n  \"main\": \"index.js\",\n  \"exports\": {\n    \".\": \"./index.js\",\n    \"./map\": \"./map.js\"\n  }\n}\n",
		"node_modules/lodash/index.js":     "export { map } from './map.js'\n",
		"node_modules/lodash/map.js":       "export default function map() {}\n",
	} {
		content, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil || string(content) != want {
			t.Fatalf("%s: content %q, error %v", name, content, err)
		}
		if runtime.GOOS != "windows" {
			info, statErr := os.Stat(filepath.Join(repo, name))
			if statErr != nil || info.Mode().Perm() != referenceInfo.Mode().Perm() {
				t.Fatalf("%s: expected mode %o, stat error %v", name, referenceInfo.Mode().Perm(), statErr)
			}
		}
	}
}

func TestWriteFileWithModesPreservesExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	root := t.TempDir()
	path := filepath.Join(root, "nested", "script.sh")
	MustWriteFileWithModes(t, path, "before", 0o700, 0o700)
	MustWriteFileWithModes(t, path, "after", 0o600, 0o750)
	for _, name := range []string{path, filepath.Dir(path)} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s: mode %o", name, info.Mode().Perm())
		}
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "after" {
		t.Fatalf("overwrite = %q, %v", content, err)
	}
}
