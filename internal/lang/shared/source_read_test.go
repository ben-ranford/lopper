package shared

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestReadSourceFileDisplayPathAndConfinement(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, "source.py")
	testutil.MustWriteFile(t, source, "import example\n")
	t.Chdir(repo)
	for _, path := range []string{source, "source.py"} {
		content, display, err := ReadSourceFile(repo, path)
		if err != nil || string(content) != "import example\n" || display != "source.py" {
			t.Fatalf("read %q: content=%q display=%q err=%v", path, content, display, err)
		}
	}
	cases := []struct {
		path string
		want error
	}{
		{filepath.Join(repo, "missing.py"), fs.ErrNotExist},
		{filepath.Join(repo, "..", "escape.py"), safeio.ErrPathEscapesRoot},
	}
	for _, tc := range cases {
		content, display, err := ReadSourceFile(repo, tc.path)
		if !errors.Is(err, tc.want) || content != nil || display != "" {
			t.Fatalf("read %q: content=%q display=%q err=%v, want %v", tc.path, content, display, err, tc.want)
		}
	}
}
