package shared

import (
	"context"
	"errors"
	"io/fs"
	"testing"
)

type stubDirEntry struct {
	name string
	dir  bool
}

func (s *stubDirEntry) Name() string { return s.name }
func (s *stubDirEntry) IsDir() bool  { return s.dir }
func (s *stubDirEntry) Type() fs.FileMode {
	if s.dir {
		return fs.ModeDir
	}
	return 0
}

func (s *stubDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

func TestApplyRootSignalsUnexpectedStatError(t *testing.T) {
	err := ApplyRootSignals(t.TempDir(), []RootSignal{{Name: "bad\x00signal", Confidence: 10}}, nil, nil)
	if err == nil {
		t.Fatalf("expected unexpected stat error to be returned")
	}
}

func TestRepoWalkerHandleAdditionalBranches(t *testing.T) {
	walkErr := errors.New("walk failed")
	walker := repoWalker{
		maxFiles: 1,
		skipDir:  func(string) bool { return false },
		visit:    func(string, fs.DirEntry) error { return nil },
	}

	if err := walker.handle(context.Background(), "repo", &stubDirEntry{name: "ignored.txt"}, walkErr); !errors.Is(err, walkErr) {
		t.Fatalf("expected walk error to be returned, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := walker.handle(ctx, "repo", &stubDirEntry{name: "ignored.txt"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context error, got %v", err)
	}

	if err := walker.handle(context.Background(), "repo/a.txt", &stubDirEntry{name: "a.txt"}, nil); err != nil {
		t.Fatalf("expected first file visit to succeed, got %v", err)
	}
	if err := walker.handle(context.Background(), "repo/b.txt", &stubDirEntry{name: "b.txt"}, nil); !errors.Is(err, fs.SkipAll) {
		t.Fatalf("expected file limit to stop walking, got %v", err)
	}
}
