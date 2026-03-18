package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestUISummaryAdditionalErrorBranches(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100},
		},
	}

	t.Run("refresh clear-screen write error", func(t *testing.T) {
		charDevice, err := os.Open("/dev/null")
		if err != nil {
			t.Skipf("open char device: %v", err)
		}
		t.Cleanup(func() {
			if closeErr := charDevice.Close(); closeErr != nil {
				t.Fatalf("close char device: %v", closeErr)
			}
		})
		if !supportsScreenRefresh(charDevice) {
			t.Skip("character device refresh detection unavailable")
		}

		summary := NewSummary(charDevice, strings.NewReader("q\n"), &stubAnalyzer{report: rep}, report.NewFormatter())
		if err := summary.Start(context.Background(), Options{RepoPath: ".", TopN: 1, PageSize: 1}); err == nil {
			t.Fatalf("expected refresh-in-place screen clear to fail on read-only output")
		}
	})

	t.Run("snapshot confirmation write error", func(t *testing.T) {
		writeErr := errors.New(uiWriteFailed)
		outputPath := filepath.Join(t.TempDir(), "snapshot.txt")
		summary := NewSummary(&failAfterWriter{failAt: 0, err: writeErr}, strings.NewReader(""), &stubAnalyzer{report: rep}, report.NewFormatter())

		if err := summary.Snapshot(context.Background(), Options{RepoPath: ".", TopN: 1, PageSize: 1}, outputPath); !errors.Is(err, writeErr) {
			t.Fatalf("expected snapshot confirmation write error, got %v", err)
		}
		if _, err := os.Stat(outputPath); err != nil {
			t.Fatalf("expected snapshot file to be written before confirmation error, got %v", err)
		}
	})
}

func TestUIDetailPlaceholderWriteErrorBranches(t *testing.T) {
	writeErr := errors.New(uiWriteFailed)

	if err := renderList[string](&failAfterWriter{failAt: 1, err: writeErr}, "Empty", nil, func(_ io.Writer, _ string) error { return nil }); !errors.Is(err, writeErr) {
		t.Fatalf("expected empty-list placeholder write error, got %v", err)
	}

	if err := printCodemod(&failAfterWriter{failAt: 1, err: writeErr}, nil); !errors.Is(err, writeErr) {
		t.Fatalf("expected nil-codemod placeholder write error, got %v", err)
	}
}
