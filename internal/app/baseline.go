package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/terminal"
)

const BaselineStoreDiscoveryFeature = "baseline-store-discovery"

func (a *App) executeBaseline(req Request) (string, error) {
	if !req.Baseline.Features.Enabled(BaselineStoreDiscoveryFeature) {
		return "", ErrBaselineFeatureDisabled
	}
	format := strings.ToLower(strings.TrimSpace(req.Baseline.Format))
	if format != "table" && format != "json" {
		return "", fmt.Errorf("invalid baseline format: %s", req.Baseline.Format)
	}
	switch req.Baseline.Action {
	case "list":
		catalog, err := report.ListBaselineSnapshots(req.Baseline.StorePath, req.Baseline.Limit)
		if err != nil {
			return "", err
		}
		if format == "json" {
			return formatBaselineJSON(catalog)
		}
		return formatBaselineCatalog(catalog), nil
	case "show":
		metadata, err := report.InspectBaselineSnapshot(req.Baseline.StorePath, req.Baseline.Key)
		if err != nil {
			return "", err
		}
		if format == "json" {
			return formatBaselineJSON(metadata)
		}
		return formatBaselineMetadata(metadata), nil
	default:
		return "", fmt.Errorf("unknown baseline action: %s", req.Baseline.Action)
	}
}

func formatBaselineJSON(value any) (string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

func formatBaselineCatalog(catalog report.BaselineCatalog) string {
	var output strings.Builder
	if len(catalog.Snapshots) == 0 {
		fmt.Fprintf(&output, "No baseline snapshots found in %s.\n", terminal.SanitizeString(catalog.Store))
	} else {
		output.WriteString("TYPE\tKEY\tCREATED\tBASELINE SCHEMA\tREPORT SCHEMA\tREPO\tDEPS\tUSED\n")
		for _, snapshot := range catalog.Snapshots {
			fmt.Fprintf(&output, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d/%d\n", snapshot.KeyType, terminal.SanitizeString(snapshot.Key), snapshot.CreatedAt.Format("2006-01-02T15:04:05Z"), terminal.SanitizeString(snapshot.BaselineSchemaVersion), terminal.SanitizeString(snapshot.ReportSchemaVersion), terminal.SanitizeString(snapshot.RepoIdentity), snapshot.Summary.DependencyCount, snapshot.Summary.UsedExportsCount, snapshot.Summary.TotalExportsCount)
		}
	}
	if len(catalog.Diagnostics) > 0 {
		output.WriteString("Diagnostics:\n")
		for _, diagnostic := range catalog.Diagnostics {
			fmt.Fprintf(&output, "- %s: %s\n", terminal.SanitizeString(diagnostic.File), terminal.SanitizeString(diagnostic.Error))
		}
	}
	return output.String()
}

func formatBaselineMetadata(snapshot report.BaselineSnapshotMetadata) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Key: %s\n", terminal.SanitizeString(snapshot.Key))
	fmt.Fprintf(&output, "Type: %s\n", snapshot.KeyType)
	if snapshot.Label != "" {
		fmt.Fprintf(&output, "Label: %s\n", terminal.SanitizeString(snapshot.Label))
	}
	if snapshot.Commit != "" {
		fmt.Fprintf(&output, "Commit: %s\n", terminal.SanitizeString(snapshot.Commit))
	}
	fmt.Fprintf(&output, "Created: %s\n", snapshot.CreatedAt.Format("2006-01-02T15:04:05Z"))
	fmt.Fprintf(&output, "Baseline schema: %s\n", terminal.SanitizeString(snapshot.BaselineSchemaVersion))
	fmt.Fprintf(&output, "Report schema: %s\n", terminal.SanitizeString(snapshot.ReportSchemaVersion))
	fmt.Fprintf(&output, "Repository: %s\n", terminal.SanitizeString(snapshot.RepoIdentity))
	fmt.Fprintf(&output, "Dependencies: %d\n", snapshot.Summary.DependencyCount)
	fmt.Fprintf(&output, "Used exports: %d/%d (%.2f%%)\n", snapshot.Summary.UsedExportsCount, snapshot.Summary.TotalExportsCount, snapshot.Summary.UsedPercent)
	fmt.Fprintf(&output, "Licenses: %d known, %d unknown, %d denied\n", snapshot.Summary.KnownLicenseCount, snapshot.Summary.UnknownLicenseCount, snapshot.Summary.DeniedLicenseCount)
	return output.String()
}
