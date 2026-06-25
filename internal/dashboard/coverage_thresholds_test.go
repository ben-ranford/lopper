package dashboard

import (
	"errors"
	"strings"
	"testing"
)

type failOnCSVWrite struct {
	call   int
	failOn int
}

func (f *failOnCSVWrite) Write(_ []string) error {
	f.call++
	if f.call == f.failOn {
		return errors.New("boom")
	}
	return nil
}

func failingCSVWriter(failOn int) func([]string) error {
	return (&failOnCSVWrite{failOn: failOn}).Write
}

func TestWriteDashboardCrossRepoRowsCSVHeaderError(t *testing.T) {
	writer := &failOnCSVWrite{failOn: 2}

	err := writeDashboardCrossRepoRowsCSV(writer.Write, []CrossRepoDependency{{
		Name:         "shared-dep",
		Count:        3,
		Repositories: []string{"api", "web", "worker"},
	}})
	if err == nil {
		t.Fatal("expected cross-repo header write to fail")
	}
}

func TestRepoRevisionHelpersCoverAllKinds(t *testing.T) {
	tests := []struct {
		name     string
		revision *RepoRevision
		wantZero bool
		wantKind string
		wantVal  string
	}{
		{name: "nil", revision: nil, wantZero: true},
		{name: "empty", revision: &RepoRevision{Branch: " ", Tag: "\t", Commit: "\n"}, wantZero: true},
		{name: "branch", revision: &RepoRevision{Branch: " release/2.0 "}, wantKind: "branch", wantVal: "release/2.0"},
		{name: "tag", revision: &RepoRevision{Tag: " v2.0.0 "}, wantKind: "tag", wantVal: "v2.0.0"},
		{name: "commit", revision: &RepoRevision{Commit: " 0123456789abcdef0123456789abcdef01234567 "}, wantKind: "commit", wantVal: "0123456789abcdef0123456789abcdef01234567"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.revision.IsZero(); got != tc.wantZero {
				t.Fatalf("IsZero() = %v, want %v", got, tc.wantZero)
			}
			if got := tc.revision.Kind(); got != tc.wantKind {
				t.Fatalf("Kind() = %q, want %q", got, tc.wantKind)
			}
			if got := tc.revision.Value(); got != tc.wantVal {
				t.Fatalf("Value() = %q, want %q", got, tc.wantVal)
			}
		})
	}
}

func requireCSVWriterError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected writer error")
	}
}

func TestDashboardSummaryAndRepoCSVWriterErrorBranches(t *testing.T) {
	requireCSVWriterError(t, writeDashboardSummaryCSV(failingCSVWriter(1), Report{}))
	requireCSVWriterError(t, writeDashboardRepoRowsCSV(failingCSVWriter(1), nil))
	requireCSVWriterError(t, writeDashboardRepoRowsCSV(failingCSVWriter(2), []RepoResult{{Name: "api"}}))
}

func TestDashboardCrossRepoCSVWriterErrorBranches(t *testing.T) {
	deps := []CrossRepoDependency{{Name: "shared"}}

	requireCSVWriterError(t, writeDashboardCrossRepoRowsCSV(failingCSVWriter(1), deps))
	requireCSVWriterError(t, writeDashboardCrossRepoRowsCSV(failingCSVWriter(3), deps))
}

func TestDashboardRemediationCSVWriterErrorBranches(t *testing.T) {
	items := []RemediationItem{{ID: "rqi-test"}}

	requireCSVWriterError(t, writeDashboardRemediationRowsCSV(failingCSVWriter(1), items))
	requireCSVWriterError(t, writeDashboardRemediationRowsCSV(failingCSVWriter(2), items))
	requireCSVWriterError(t, writeDashboardRemediationRowsCSV(failingCSVWriter(3), items))
}

func TestDashboardBaselineCSVWriterErrorBranches(t *testing.T) {
	keys := &BaselineComparison{BaselineKey: "base", CurrentKey: "head"}
	summary := &BaselineComparison{BaselineKey: "base"}
	repos := &BaselineComparison{BaselineKey: "base", RepoDeltas: []RepoDelta{{Name: "api"}}}

	requireCSVWriterError(t, writeDashboardBaselineRowsCSV(failingCSVWriter(2), keys))
	requireCSVWriterError(t, writeDashboardBaselineRowsCSV(failingCSVWriter(3), keys))
	requireCSVWriterError(t, writeDashboardBaselineRowsCSV(failingCSVWriter(4), summary))
	requireCSVWriterError(t, writeDashboardBaselineRowsCSV(failingCSVWriter(13), repos))
	requireCSVWriterError(t, writeDashboardBaselineRowsCSV(failingCSVWriter(14), repos))
}

func TestDashboardBaselineRemediationCSVWriterErrorBranches(t *testing.T) {
	items := []RemediationItemDelta{{ID: "rqi-test"}}

	requireCSVWriterError(t, writeDashboardBaselineRemediationRowsCSV(failingCSVWriter(1), items))
	requireCSVWriterError(t, writeDashboardBaselineRemediationRowsCSV(failingCSVWriter(2), items))
	requireCSVWriterError(t, writeDashboardBaselineRemediationRowsCSV(failingCSVWriter(3), items))
}

func TestFormatReportCSVRevisionKinds(t *testing.T) {
	reportData := Report{
		Repos: []RepoResult{
			{Name: "branch", Revision: &RepoRevision{Branch: "release/2.0"}},
			{Name: "commit", Revision: &RepoRevision{Commit: "0123456789abcdef0123456789abcdef01234567"}},
			{Name: "unpinned"},
		},
	}
	output, err := FormatReport(reportData, FormatCSV)
	if err != nil {
		t.Fatalf("format dashboard csv: %v", err)
	}
	for _, want := range []string{"branch:release/2.0", "commit:0123456789abcdef0123456789abcdef01234567", "unpinned"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected csv output to contain %q, got %q", want, output)
		}
	}
}
