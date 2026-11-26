package js

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestDetectLicenseFromFilesSkipsUnreadableDescendantDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permission fixture is not reliable on this platform")
	}
	root := t.TempDir()
	blocked := filepath.Join(root, "A-blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatalf("create blocked directory: %v", err)
	}
	defer func() {
		if err := os.Chmod(blocked, 0o700); err != nil {
			t.Error(err)
		}
	}()
	testutil.MustWriteFile(t, filepath.Join(root, "LICENSE"), "MIT License")
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("restrict directory: %v", err)
	}
	if _, err := os.ReadDir(blocked); !errors.Is(err, fs.ErrPermission) {
		t.Skip("filesystem did not enforce directory permissions")
	}

	license, status := detectLicenseFromFilesWithStatus(root)
	if !status.skippedDirectory {
		t.Fatalf("expected unreadable directory status, got %#v", status)
	}
	if license == nil || license.SPDX != "MIT" || license.Source != "license-file" {
		t.Fatalf("expected later license evidence after skipped directory, got %#v", license)
	}
	if len(status.warning()) != 1 {
		t.Fatalf("expected incomplete scan warning, got %#v", status)
	}
}
