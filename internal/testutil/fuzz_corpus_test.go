package testutil

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadByteFuzzCorpus(t *testing.T) {
	dir := t.TempDir()
	MustWriteFile(t, filepath.Join(dir, "b"), "go test fuzz v1\n[]byte(`second\\nseed`)\n")
	MustWriteFile(t, filepath.Join(dir, "a"), "go test fuzz v1\n[]byte(\"first seed\")\n")

	seeds := LoadByteFuzzCorpus(t, dir)
	if len(seeds) != 2 {
		t.Fatalf("expected two seeds, got %#v", seeds)
	}
	if seeds[0].Name != "a" || string(seeds[0].Data) != "first seed" {
		t.Fatalf("unexpected first seed %#v", seeds[0])
	}
	if seeds[1].Name != "b" || string(seeds[1].Data) != "second\\nseed" {
		t.Fatalf("unexpected second seed %#v", seeds[1])
	}
}

func TestMustWritePaddedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "padded.txt")

	MustWritePaddedFile(t, path, "hello", 32)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat padded file: %v", err)
	}
	if info.Size() != 32 {
		t.Fatalf("padded file size = %d, want 32", info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read padded file: %v", err)
	}
	if string(data[:5]) != "hello" {
		t.Fatalf("padded file prefix = %q, want hello", string(data[:5]))
	}
}

func TestLoadByteFuzzCorpusReportsMissingDirectories(t *testing.T) {
	expectFatal(t, "read fuzz corpus", func(tb *fakeTB) {
		loadByteFuzzCorpus(tb, filepath.Join(t.TempDir(), "missing"))
	})
}

func TestLoadByteFuzzCorpusReportsOpenRootFailures(t *testing.T) {
	patchTestutilSeams(t)
	openRoot = func(name string) (rootHandle, error) {
		return nil, io.EOF
	}

	expectFatal(t, "open fuzz corpus root", func(tb *fakeTB) {
		loadByteFuzzCorpus(tb, t.TempDir())
	})
}

func TestLoadByteFuzzCorpusReportsNestedDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	expectFatal(t, "contains nested directory", func(tb *fakeTB) {
		loadByteFuzzCorpus(tb, dir)
	})
}

func TestReadRootedFuzzSeedReportsOpenFailures(t *testing.T) {
	root := &stubRoot{
		openFunc: func(name string) (rootFile, error) { return nil, io.EOF },
		openFileFunc: func(name string, flag int, perm os.FileMode) (rootFile, error) {
			return nil, io.EOF
		},
		closeFunc: func() error { return nil },
	}

	expectFatal(t, "open fuzz seed", func(tb *fakeTB) {
		readRootedFuzzSeed(tb, root, t.TempDir(), "seed")
	})
}

func TestReadRootedFuzzSeedReportsReadFailures(t *testing.T) {
	root := &stubRoot{
		openFunc: func(name string) (rootFile, error) {
			return &stubRootFile{
				readFunc:        func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF },
				writeStringFunc: func(string) (int, error) { return 0, nil },
				closeFunc:       func() error { return nil },
			}, nil
		},
		openFileFunc: func(name string, flag int, perm os.FileMode) (rootFile, error) {
			return nil, io.EOF
		},
		closeFunc: func() error { return nil },
	}

	expectFatal(t, "read fuzz seed", func(tb *fakeTB) {
		readRootedFuzzSeed(tb, root, t.TempDir(), "seed")
	})
}

func TestReadRootedFuzzSeedReportsCloseFailures(t *testing.T) {
	root := &stubRoot{
		openFunc: func(name string) (rootFile, error) {
			return &stubRootFile{
				readFunc:        func(p []byte) (int, error) { copy(p, "seed"); return 4, io.EOF },
				writeStringFunc: func(string) (int, error) { return 0, nil },
				closeFunc:       func() error { return io.ErrClosedPipe },
			}, nil
		},
		openFileFunc: func(name string, flag int, perm os.FileMode) (rootFile, error) {
			return nil, io.EOF
		},
		closeFunc: func() error { return nil },
	}

	expectFatal(t, "close fuzz seed", func(tb *fakeTB) {
		readRootedFuzzSeed(tb, root, t.TempDir(), "seed")
	})
}

func TestLoadByteFuzzCorpusReportsRootCloseFailures(t *testing.T) {
	patchTestutilSeams(t)
	openRoot = func(name string) (rootHandle, error) {
		return &stubRoot{
			openFunc: func(name string) (rootFile, error) {
				return &stubRootFile{
					readFunc:        func(p []byte) (int, error) { return copy(p, "go test fuzz v1\n[]byte(\"seed\")\n"), io.EOF },
					writeStringFunc: func(string) (int, error) { return 0, nil },
					closeFunc:       func() error { return nil },
				}, nil
			},
			openFileFunc: func(name string, flag int, perm os.FileMode) (rootFile, error) {
				return nil, io.EOF
			},
			closeFunc: func() error { return io.ErrClosedPipe },
		}, nil
	}
	readDir = func(name string) ([]os.DirEntry, error) {
		return []os.DirEntry{mustDirEntry(t, t.TempDir(), "seed")}, nil
	}

	expectFatal(t, "close fuzz corpus root", func(tb *fakeTB) {
		loadByteFuzzCorpus(tb, t.TempDir())
	})
}

func TestParseByteFuzzSeedReportsMissingPayloadLines(t *testing.T) {
	expectFatal(t, "missing a payload line", func(tb *fakeTB) {
		parseByteFuzzSeed(tb, filepath.Join(t.TempDir(), "seed"), "go test fuzz v1")
	})
}

func TestParseByteFuzzSeedReportsUnexpectedHeaders(t *testing.T) {
	expectFatal(t, "unexpected header", func(tb *fakeTB) {
		parseByteFuzzSeed(tb, filepath.Join(t.TempDir(), "seed"), "nope\n[]byte(\"seed\")\n")
	})
}

func TestParseByteFuzzSeedReportsInvalidWrappers(t *testing.T) {
	expectFatal(t, "must wrap a []byte literal", func(tb *fakeTB) {
		parseByteFuzzSeed(tb, filepath.Join(t.TempDir(), "seed"), "go test fuzz v1\nstring(\"seed\")\n")
	})
}

func TestParseByteFuzzSeedReportsUnquoteFailures(t *testing.T) {
	expectFatal(t, "unquote fuzz seed", func(tb *fakeTB) {
		parseByteFuzzSeed(tb, filepath.Join(t.TempDir(), "seed"), "go test fuzz v1\n[]byte(\"\\xzz\")\n")
	})
}

type staticDirEntry string

func (e *staticDirEntry) Name() string               { return string(*e) }
func (e *staticDirEntry) IsDir() bool                { return false }
func (e *staticDirEntry) Type() os.FileMode          { return 0 }
func (e *staticDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func mustDirEntry(t *testing.T, dir string, name string) os.DirEntry {
	t.Helper()
	path := filepath.Join(dir, name)
	MustWriteFile(t, path, "")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, candidate := range entries {
		if candidate.Name() == name {
			return candidate
		}
	}
	if path != "" {
		t.Fatalf("missing dir entry %s", path)
	}
	fallback := staticDirEntry(name)
	return &fallback
}
