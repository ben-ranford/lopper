package app

import (
	"context"
	"sync"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

const (
	sharedDependencyName = "shared-lib"
	singleRepoPath       = "./repo"
)

type mapAnalyzer struct {
	mu      sync.Mutex
	reports map[string]report.Report
	errs    map[string]error
	calls   []analysis.Request
}

func (m *mapAnalyzer) Analyse(_ context.Context, req analysis.Request) (report.Report, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	m.mu.Unlock()

	if err, ok := m.errs[req.RepoPath]; ok {
		return report.Report{}, err
	}
	if reportData, ok := m.reports[req.RepoPath]; ok {
		return reportData, nil
	}
	return report.Report{}, nil
}
