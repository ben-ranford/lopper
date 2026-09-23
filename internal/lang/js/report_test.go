package js

import (
	"context"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestReportFormatContainsDependency(t *testing.T) {
	repoPath, _, _ := setupLodashFixture(t, "import { map } from 'lodash'\nmap([1], (x) => x)\n")
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
