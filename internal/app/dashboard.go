package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/dashboard"
)

type resolvedDashboardRequest struct {
	repos      []dashboard.RepoInput
	format     dashboard.Format
	outputPath string
}

func (a *App) executeDashboard(ctx context.Context, req Request) (string, error) {
	if a.Analyzer == nil {
		return "", fmt.Errorf("dashboard analyzer is not configured")
	}

	resolved, err := resolveDashboardRequest(req.Dashboard)
	if err != nil {
		return "", err
	}

	analyses := a.runDashboardAnalyses(ctx, req.Dashboard, resolved.repos)
	aggregated := dashboard.Aggregate(time.Now(), analyses)

	formatted, err := dashboard.FormatReport(aggregated, resolved.format)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(resolved.outputPath) == "" {
		return formatted, nil
	}

	outputPath := strings.TrimSpace(resolved.outputPath)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o750); err != nil {
		return "", err
	}
	if err := os.WriteFile(outputPath, []byte(formatted), 0o600); err != nil {
		return "", err
	}
	return "dashboard report written to " + outputPath, nil
}

func resolveDashboardRequest(req DashboardRequest) (resolvedDashboardRequest, error) {
	repos := make([]dashboard.RepoInput, 0)
	for _, repo := range req.Repos {
		path := strings.TrimSpace(repo.Path)
		if path == "" {
			continue
		}
		name := strings.TrimSpace(repo.Name)
		if name == "" {
			name = inferDashboardRepoName(path)
		}
		repos = append(repos, dashboard.RepoInput{
			Name:     name,
			Path:     path,
			Language: strings.TrimSpace(repo.Language),
		})
	}

	configFormat := ""
	configPath := strings.TrimSpace(req.ConfigPath)
	if configPath != "" {
		loadedConfig, err := dashboard.LoadConfig(configPath)
		if err != nil {
			return resolvedDashboardRequest{}, err
		}
		configFormat = loadedConfig.Dashboard.Output
		if len(repos) == 0 {
			configRepos, err := reposFromDashboardConfig(loadedConfig)
			if err != nil {
				return resolvedDashboardRequest{}, err
			}
			repos = append(repos, configRepos...)
		}
	}

	if len(repos) == 0 {
		return resolvedDashboardRequest{}, fmt.Errorf("dashboard requires at least one repo via --repos or --config")
	}

	defaultLanguage := strings.TrimSpace(req.DefaultLanguage)
	if defaultLanguage == "" {
		defaultLanguage = "auto"
	}
	for index := range repos {
		if strings.TrimSpace(repos[index].Language) == "" {
			repos[index].Language = defaultLanguage
		}
	}

	formatValue := strings.TrimSpace(req.Format)
	if formatValue == "" {
		formatValue = strings.TrimSpace(configFormat)
	}
	format, err := dashboard.ParseFormat(formatValue)
	if err != nil {
		return resolvedDashboardRequest{}, err
	}

	return resolvedDashboardRequest{
		repos:      repos,
		format:     format,
		outputPath: strings.TrimSpace(req.OutputPath),
	}, nil
}

func reposFromDashboardConfig(config dashboard.LoadedConfig) ([]dashboard.RepoInput, error) {
	repos := make([]dashboard.RepoInput, 0, len(config.Dashboard.Repos))
	for _, repo := range config.Dashboard.Repos {
		repoPath := strings.TrimSpace(repo.Path)
		repoURL := strings.TrimSpace(repo.RepoURL)
		if repoPath == "" {
			if repoURL != "" {
				return nil, fmt.Errorf("dashboard config repo %q uses repoUrl, which is not supported yet", repoURL)
			}
			return nil, fmt.Errorf("dashboard config repo is missing path")
		}
		if !filepath.IsAbs(repoPath) {
			repoPath = filepath.Join(config.ConfigDir, repoPath)
		}
		name := strings.TrimSpace(repo.Name)
		if name == "" {
			name = inferDashboardRepoName(repoPath)
		}
		repos = append(repos, dashboard.RepoInput{
			Name:     name,
			Path:     repoPath,
			Language: strings.TrimSpace(repo.Language),
		})
	}
	return repos, nil
}

func inferDashboardRepoName(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return strings.TrimSpace(path)
	}
	return base
}

func (a *App) runDashboardAnalyses(ctx context.Context, request DashboardRequest, repos []dashboard.RepoInput) []dashboard.RepoAnalysis {
	results := make([]dashboard.RepoAnalysis, len(repos))
	if len(repos) == 0 {
		return results
	}

	topN := request.TopN
	if topN <= 0 {
		topN = 20
	}

	maxWorkers := runtime.NumCPU()
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if len(repos) < maxWorkers {
		maxWorkers = len(repos)
	}

	workers := make(chan struct{}, maxWorkers)
	var waitGroup sync.WaitGroup

	for index, repoInput := range repos {
		index := index
		repoInput := repoInput
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			workers <- struct{}{}
			defer func() { <-workers }()

			reportData, err := a.Analyzer.Analyse(ctx, analysis.Request{
				RepoPath:       repoInput.Path,
				TopN:           topN,
				ScopeMode:      analysis.ScopeModeRepo,
				Language:       repoInput.Language,
				RuntimeProfile: "node-import",
			})

			results[index] = dashboard.RepoAnalysis{
				Input:  repoInput,
				Report: reportData,
				Err:    err,
			}
		}()
	}

	waitGroup.Wait()
	return results
}
