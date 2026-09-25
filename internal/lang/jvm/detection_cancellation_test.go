package jvm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestJVMDetectionCancellationAfterRootSignals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := afterJVMDetectRootSignals
	t.Cleanup(func() { afterJVMDetectRootSignals = original })
	afterJVMDetectRootSignals = func(string) error { cancel(); return nil }
	_, err := NewAdapter().DetectWithConfidence(ctx, t.TempDir())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestJVMDetectionCancellationDuringDirectoryRead(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "Main.java"), []byte("class Main {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	detection := language.Detection{}
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &detection, defaultJVMDetectionBudget())
	walker.openDirectory = func(root jvmDetectionRoot, path string) (jvmDetectionDirectory, error) {
		directory, err := openJVMDetectionDirectory(root, path)
		if err != nil {
			return nil, err
		}
		return &cancelingDetectionDirectory{jvmDetectionDirectory: directory, cancel: cancel}, nil
	}
	if err := walker.walk(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if detection.Matched {
		t.Fatalf("canceled traversal processed source: %#v", detection)
	}
}

type cancelingDetectionDirectory struct {
	jvmDetectionDirectory
	cancel context.CancelFunc
}

func (d *cancelingDetectionDirectory) ReadDir(count int) ([]fs.DirEntry, error) {
	entries, err := d.jvmDetectionDirectory.ReadDir(count)
	d.cancel()
	return entries, err
}
