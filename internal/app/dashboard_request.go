package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/featureflags"
)

type resolvedDashboardRequest struct {
	repos             []dashboard.RepoInput
	format            dashboard.Format
	outputPath        string
	baselineStorePath string
	baselineKey       string
	baselineLabel     string
	saveBaseline      bool
}

func resolveDashboardRequest(req DashboardRequest) (resolvedDashboardRequest, error) {
	repos := normalizedDashboardRepos(req.Repos)
	configFormat := ""
	loadedConfig, hasConfig, err := loadDashboardConfig(req.ConfigPath)
	if err != nil {
		return resolvedDashboardRequest{}, err
	}
	if hasConfig {
		configFormat = loadedConfig.Dashboard.Output
		if strings.TrimSpace(req.BaselineStorePath) == "" {
			req.BaselineStorePath = resolveDashboardConfigPath(loadedConfig.ConfigDir, loadedConfig.Dashboard.BaselineStore)
		}
		if len(repos) == 0 {
			repos, err = reposFromDashboardConfig(loadedConfig, &req.Features)
			if err != nil {
				return resolvedDashboardRequest{}, err
			}
		}
	}
	if len(repos) == 0 {
		return resolvedDashboardRequest{}, fmt.Errorf("dashboard requires at least one repo via --repos or --config")
	}

	applyDefaultDashboardLanguage(repos, req.DefaultLanguage)
	format, err := resolveDashboardFormat(req.Format, configFormat)
	if err != nil {
		return resolvedDashboardRequest{}, err
	}

	return resolvedDashboardRequest{
		repos:             repos,
		format:            format,
		outputPath:        strings.TrimSpace(req.OutputPath),
		baselineStorePath: strings.TrimSpace(req.BaselineStorePath),
		baselineKey:       strings.TrimSpace(req.BaselineKey),
		baselineLabel:     strings.TrimSpace(req.BaselineLabel),
		saveBaseline:      req.SaveBaseline,
	}, nil
}

func normalizedDashboardRepos(repos []DashboardRepo) []dashboard.RepoInput {
	normalized := make([]dashboard.RepoInput, 0, len(repos))
	for _, repo := range repos {
		path := strings.TrimSpace(repo.Path)
		if path == "" {
			continue
		}
		name := strings.TrimSpace(repo.Name)
		if name == "" {
			name = inferDashboardRepoName(path)
		}
		normalized = append(normalized, dashboard.RepoInput{
			Name:     name,
			Path:     path,
			Language: strings.TrimSpace(repo.Language),
		})
	}
	return normalized
}

func loadDashboardConfig(configPath string) (dashboard.LoadedConfig, bool, error) {
	trimmedConfigPath := strings.TrimSpace(configPath)
	if trimmedConfigPath == "" {
		return dashboard.LoadedConfig{}, false, nil
	}
	loadedConfig, err := dashboard.LoadConfig(trimmedConfigPath)
	if err != nil {
		return dashboard.LoadedConfig{}, false, err
	}
	return loadedConfig, true, nil
}

func applyDefaultDashboardLanguage(repos []dashboard.RepoInput, defaultLanguage string) {
	resolvedLanguage := strings.TrimSpace(defaultLanguage)
	if resolvedLanguage == "" {
		resolvedLanguage = "auto"
	}
	for index := range repos {
		if strings.TrimSpace(repos[index].Language) == "" {
			repos[index].Language = resolvedLanguage
		}
	}
}

func resolveDashboardFormat(flagFormat, configFormat string) (dashboard.Format, error) {
	formatValue := strings.TrimSpace(flagFormat)
	if formatValue == "" {
		formatValue = strings.TrimSpace(configFormat)
	}
	return dashboard.ParseFormat(formatValue)
}

func reposFromDashboardConfig(config dashboard.LoadedConfig, features *featureflags.Set) ([]dashboard.RepoInput, error) {
	repos := make([]dashboard.RepoInput, 0, len(config.Dashboard.Repos))
	for _, repo := range config.Dashboard.Repos {
		input, err := dashboardRepoInputFromConfig(config.ConfigDir, repo, features)
		if err != nil {
			return nil, err
		}
		repos = append(repos, input)
	}
	return repos, nil
}

func dashboardRepoInputFromConfig(configDir string, repo dashboard.ConfigRepo, features *featureflags.Set) (dashboard.RepoInput, error) {
	repoPath := strings.TrimSpace(repo.Path)
	repoURL := strings.TrimSpace(repo.RepoURL)
	revision, err := normalizeDashboardRepoRevision(dashboard.RepoRevision{
		Branch: repo.Branch,
		Tag:    repo.Tag,
		Commit: repo.Commit,
	})
	if err != nil {
		return dashboard.RepoInput{}, fmt.Errorf("dashboard config repo revision is invalid: %w", err)
	}
	if repoPath != "" && !revision.IsZero() {
		return dashboard.RepoInput{}, fmt.Errorf("dashboard config repo revision fields require repoUrl")
	}
	switch {
	case repoPath != "" && repoURL != "":
		return dashboard.RepoInput{}, fmt.Errorf("dashboard config repo cannot define both path and repoUrl")
	case repoPath == "" && repoURL == "":
		return dashboard.RepoInput{}, fmt.Errorf("dashboard config repo is missing path or repoUrl")
	case repoURL != "":
		return dashboardRepoInputFromURL(repo, repoURL, revision, features)
	default:
		return dashboardRepoInputFromPath(configDir, repo, repoPath), nil
	}
}

func dashboardRepoInputFromURL(repo dashboard.ConfigRepo, repoURL string, revision dashboard.RepoRevision, features *featureflags.Set) (dashboard.RepoInput, error) {
	if features == nil || !features.Enabled(DashboardRemoteReposPreviewFeature) {
		return dashboard.RepoInput{}, fmt.Errorf("dashboard config repoUrl requires feature %s", DashboardRemoteReposPreviewFeature)
	}
	spec, err := parseDashboardRepoURL(repoURL)
	if err != nil {
		return dashboard.RepoInput{}, fmt.Errorf("dashboard config repoUrl %q is not allowed: %w", repoURL, err)
	}
	name := strings.TrimSpace(repo.Name)
	if name == "" {
		name = spec.name
	}
	return dashboard.RepoInput{
		Name:     name,
		RepoURL:  spec.normalized,
		Revision: revision,
		Language: strings.TrimSpace(repo.Language),
	}, nil
}

func dashboardRepoInputFromPath(configDir string, repo dashboard.ConfigRepo, repoPath string) dashboard.RepoInput {
	if !filepath.IsAbs(repoPath) {
		repoPath = filepath.Join(configDir, repoPath)
	}
	name := strings.TrimSpace(repo.Name)
	if name == "" {
		name = inferDashboardRepoName(repoPath)
	}
	return dashboard.RepoInput{
		Name:     name,
		Path:     repoPath,
		Language: strings.TrimSpace(repo.Language),
	}
}

func inferDashboardRepoName(path string) string {
	base := filepath.Base(strings.TrimSpace(path))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return strings.TrimSpace(path)
	}
	return base
}

func resolveDashboardConfigPath(configDir, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return trimmed
	}
	return filepath.Join(configDir, trimmed)
}
