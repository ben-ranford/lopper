package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/advisory"
	"github.com/ben-ranford/lopper/internal/report"
)

func (a *App) executeAdvisory(ctx context.Context, req Request) (string, error) {
	if !req.Advisory.Features.Enabled(report.AdvisoryOSVSyncPreviewFeature) {
		return "", fmt.Errorf("advisory cache workflows require --enable-feature %s", report.AdvisoryOSVSyncPreviewFeature)
	}
	switch strings.TrimSpace(req.Advisory.Command) {
	case "sync":
		return executeAdvisorySync(ctx, req.Advisory)
	case "status":
		return executeAdvisoryStatus(req.Advisory)
	default:
		return "", fmt.Errorf("unknown advisory command: %s", req.Advisory.Command)
	}
}

func executeAdvisorySync(ctx context.Context, req AdvisoryRequest) (string, error) {
	if strings.TrimSpace(req.Provider) != "osv" {
		return "", fmt.Errorf("unsupported advisory sync provider: %s", req.Provider)
	}
	snapshot, err := advisory.SyncOSV(ctx, advisory.SyncOptions{
		SourceURL: req.SourceURL,
		CachePath: req.CachePath,
		Now:       time.Now().UTC(),
	})
	if err != nil {
		return "", err
	}
	return persistJSONCommandOutput(snapshot, req.OutputPath, "advisory sync result")
}

func executeAdvisoryStatus(req AdvisoryRequest) (string, error) {
	manifest, err := advisory.LoadCacheManifest(req.CachePath)
	if err != nil {
		return "", err
	}
	return persistJSONCommandOutput(manifest, req.OutputPath, "advisory cache status")
}

func persistJSONCommandOutput(value any, outputPath string, description string) (string, error) {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	payload = append(payload, '\n')
	return persistCommandOutput(string(payload), outputPath, description)
}
