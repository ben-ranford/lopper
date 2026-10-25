//go:build !windows

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFixtureCacheStartupDoesNotExecuteGoShim(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "go-executed")
	writeFixtureFileMode(t, root, "go", "#!/bin/sh\nprintf invoked > \"$FIXTURE_GO_MARKER\"\nexit 64\n", 0o755)
	bridge, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), bridge, "-test.run=^$", "-test.v")
	command.Env = append(os.Environ(), "GOCACHE=", "PATH="+root, "FIXTURE_GO_MARKER="+marker)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("non-Go test startup invoked a fixture Go shim: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("test startup executed Go shim: %v", err)
	}
}
