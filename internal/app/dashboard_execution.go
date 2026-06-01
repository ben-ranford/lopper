package app

import (
	"context"
	"runtime"
	"sync"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/dashboard"
)

type dashboardAnalysisJob struct {
	index        int
	repoInput    dashboard.RepoInput
	analyseInput analysis.Request
}

type dashboardExecutionPlan struct {
	jobs       []dashboardAnalysisJob
	maxWorkers int
}

func (a *App) runDashboardAnalyses(ctx context.Context, request DashboardRequest, repos []dashboard.RepoInput) []dashboard.RepoAnalysis {
	return a.executeDashboardAnalysisPlan(ctx, planDashboardExecution(request, repos))
}

func planDashboardExecution(request DashboardRequest, repos []dashboard.RepoInput) dashboardExecutionPlan {
	topN := request.TopN
	if topN <= 0 {
		topN = 20
	}

	jobs := make([]dashboardAnalysisJob, 0, len(repos))
	for index, repoInput := range repos {
		jobs = append(jobs, dashboardAnalysisJob{
			index:     index,
			repoInput: repoInput,
			analyseInput: analysis.Request{
				RepoPath:       repoInput.Path,
				TopN:           topN,
				ScopeMode:      analysis.ScopeModeRepo,
				Language:       repoInput.Language,
				Features:       request.Features,
				RuntimeProfile: "node-import",
			},
		})
	}

	maxWorkers := runtime.NumCPU()
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if len(jobs) < maxWorkers {
		maxWorkers = len(jobs)
	}

	return dashboardExecutionPlan{
		jobs:       jobs,
		maxWorkers: maxWorkers,
	}
}

func (a *App) executeDashboardAnalysisPlan(ctx context.Context, plan dashboardExecutionPlan) []dashboard.RepoAnalysis {
	results := make([]dashboard.RepoAnalysis, len(plan.jobs))
	if len(plan.jobs) == 0 {
		return results
	}

	jobs := make(chan dashboardAnalysisJob)
	var waitGroup sync.WaitGroup

	for workerIndex := 0; workerIndex < plan.maxWorkers; workerIndex++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for job := range jobs {
				reportData, err := a.Analyzer.Analyse(ctx, job.analyseInput)
				results[job.index] = dashboard.RepoAnalysis{
					Input:  job.repoInput,
					Report: reportData,
					Err:    err,
				}
			}
		}()
	}

	for _, job := range plan.jobs {
		jobs <- job
	}
	close(jobs)

	waitGroup.Wait()
	return results
}
