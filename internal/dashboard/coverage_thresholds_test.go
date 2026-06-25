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

func TestDashboardCSVWriterErrorBranches(t *testing.T) {
	if err := writeDashboardSummaryCSV((&failOnCSVWrite{failOn: 1}).Write, Report{}); err == nil {
		t.Fatal("expected summary writer error")
	}
	if err := writeDashboardRepoRowsCSV((&failOnCSVWrite{failOn: 1}).Write, nil); err == nil {
		t.Fatal("expected repo header writer error")
	}
	if err := writeDashboardRepoRowsCSV((&failOnCSVWrite{failOn: 2}).Write, []RepoResult{{Name: "api"}}); err == nil {
		t.Fatal("expected repo row writer error")
	}
	if err := writeDashboardCrossRepoRowsCSV((&failOnCSVWrite{failOn: 1}).Write, []CrossRepoDependency{{Name: "shared"}}); err == nil {
		t.Fatal("expected cross-repo separator writer error")
	}
	if err := writeDashboardCrossRepoRowsCSV((&failOnCSVWrite{failOn: 3}).Write, []CrossRepoDependency{{Name: "shared"}}); err == nil {
		t.Fatal("expected cross-repo row writer error")
	}
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
