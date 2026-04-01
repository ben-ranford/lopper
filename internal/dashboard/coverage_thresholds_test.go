package dashboard

import (
	"errors"
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
