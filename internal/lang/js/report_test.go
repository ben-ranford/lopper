package js

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestReportFormatContainsDependency(t *testing.T) {
	repoPath := filepath.Join("..", "..", "..", "testdata", "js", "esm")
	adapter := NewAdapter()
	reportData, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath:   repoPath,
		Dependency: "lodash",
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}

	formatted, err := report.NewFormatter().Format(reportData, report.FormatTable)
	if err != nil {
		t.Fatalf("format: %v", err)
	}

	if !strings.Contains(formatted, "lodash") {
		t.Fatalf("expected report output to contain dependency name")
	}
}
