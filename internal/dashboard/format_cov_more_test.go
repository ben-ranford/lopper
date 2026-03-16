package dashboard

import (
	"encoding/csv"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

type failingWriter struct{}

func (f *failingWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("boom")
}

func TestDashboardAdditionalBranchCoverage(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{Name: strings.Repeat("x", 5000), WasteCandidatePercent: math.NaN()},
		},
		Summary: Summary{TotalRepos: 1, TotalDeps: 1},
	}
	if _, err := FormatReport(reportData, FormatJSON); err == nil {
		t.Fatalf("expected json format to fail for NaN payload")
	}

	repoWriter := csv.NewWriter(&failingWriter{})
	if writeDashboardRepoRowsCSV(repoWriter, []RepoResult{{Name: strings.Repeat("x", 5000)}}) == nil {
		t.Fatalf("expected repo row csv write to fail")
	}

	crossRepoWriter := csv.NewWriter(&failingWriter{})
	if writeDashboardCrossRepoRowsCSV(crossRepoWriter, []CrossRepoDependency{{Name: strings.Repeat("x", 5000), Count: 3}}) == nil {
		t.Fatalf("expected cross-repo csv write to fail")
	}

	if got := metricHTML(`<label>`, `<value>`); !strings.Contains(got, "&lt;label&gt;") || !strings.Contains(got, "&lt;value&gt;") {
		t.Fatalf("expected metric html escaping, got %q", got)
	}
}

func TestDashboardAggregateSkipsBlankDependencyNames(t *testing.T) {
	reportData := Aggregate(time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC), []RepoAnalysis{{
		Input: RepoInput{Name: testRepoA, Path: "./a"},
		Report: report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "   "},
			},
		},
	}})

	if len(reportData.CrossRepoDeps) != 0 {
		t.Fatalf("expected blank dependency names to be excluded from cross-repo index, got %#v", reportData.CrossRepoDeps)
	}
}
