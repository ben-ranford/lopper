package app

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/dashboard"
)

type dashboardMaterializationPlan struct {
	format     dashboard.Format
	outputPath string
}

func planDashboardMaterialization(resolved resolvedDashboardRequest) dashboardMaterializationPlan {
	return dashboardMaterializationPlan{
		format:     resolved.format,
		outputPath: resolved.outputPath,
	}
}

func materializeDashboardReport(analyses []dashboard.RepoAnalysis, plan dashboardMaterializationPlan, now time.Time) (string, error) {
	aggregated := dashboard.Aggregate(now, analyses)
	formatted, err := dashboard.FormatReport(aggregated, plan.format)
	if err != nil {
		return "", err
	}
	return persistDashboardOutput(formatted, plan.outputPath)
}

func persistDashboardOutput(formatted, outputPath string) (string, error) {
	trimmedOutputPath := strings.TrimSpace(outputPath)
	if trimmedOutputPath == "" {
		return formatted, nil
	}

	if err := os.MkdirAll(filepath.Dir(trimmedOutputPath), 0o750); err != nil {
		return "", err
	}
	if err := os.WriteFile(trimmedOutputPath, []byte(formatted), 0o600); err != nil {
		return "", err
	}
	return "dashboard report written to " + trimmedOutputPath, nil
}
