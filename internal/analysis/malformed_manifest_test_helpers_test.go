package analysis

import (
	"context"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func analyseMalformedManifestFixture(t *testing.T, repo, language, dependency string) report.Report {
	return analyseMalformedManifestFixtureInScope(t, repo, language, dependency, "")
}

func analyseMalformedManifestFixtureInScope(t *testing.T, repo, language, dependency, scopeMode string) report.Report {
	t.Helper()
	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Language:   language,
		ScopeMode:  scopeMode,
		Dependency: dependency,
		Cache:      &CacheOptions{Enabled: false},
	})
	if err != nil {
		t.Fatalf("analyse %s malformed-manifest fixture: %v", language, err)
	}
	return reportData
}
