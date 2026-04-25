package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
)

func parseDashboard(args []string, req app.Request) (app.Request, error) {
	normalizedArgs, err := normalizeArgs(args)
	if err != nil {
		return req, err
	}
	args = normalizedArgs

	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	reposFlag := fs.String("repos", "", "comma-separated repository paths")
	configFlag := fs.String("config", req.Dashboard.ConfigPath, "dashboard config file path")
	formatFlag := fs.String("format", "", "dashboard output format")
	topFlag := fs.Int("top", req.Dashboard.TopN, "top N dependencies per repo")
	languageFlag := fs.String("language", "", "default language adapter for repos")
	outputFlag := fs.String("output", req.Dashboard.OutputPath, "output file path")
	outputShortFlag := fs.String("o", req.Dashboard.OutputPath, "output file path")

	if err := parseFlagSet(fs, args); err != nil {
		return req, err
	}
	if fs.NArg() > 0 {
		return req, fmt.Errorf("unexpected arguments for dashboard")
	}
	if *topFlag <= 0 {
		return req, fmt.Errorf("--top must be > 0")
	}

	outputPath := strings.TrimSpace(*outputFlag)
	shortOutputPath := strings.TrimSpace(*outputShortFlag)
	if outputPath != "" && shortOutputPath != "" && outputPath != shortOutputPath {
		return req, fmt.Errorf("--output and -o must match when both are provided")
	}
	if outputPath == "" {
		outputPath = shortOutputPath
	}

	repos := splitRepoList(*reposFlag)
	if len(repos) == 0 && strings.TrimSpace(*configFlag) == "" {
		return req, fmt.Errorf("dashboard requires --repos or --config")
	}
	resolvedFeatures, err := resolveDefaultFeatureSet()
	if err != nil {
		return req, err
	}

	dashboardRepos := make([]app.DashboardRepo, 0, len(repos))
	for _, repoPath := range repos {
		dashboardRepos = append(dashboardRepos, app.DashboardRepo{Path: repoPath})
	}

	req.Mode = app.ModeDashboard
	req.Dashboard = app.DashboardRequest{
		Repos:           dashboardRepos,
		ConfigPath:      strings.TrimSpace(*configFlag),
		Format:          strings.TrimSpace(*formatFlag),
		OutputPath:      outputPath,
		TopN:            *topFlag,
		DefaultLanguage: strings.TrimSpace(*languageFlag),
		Features:        resolvedFeatures,
	}

	return req, nil
}
