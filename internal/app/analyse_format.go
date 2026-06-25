package app

import (
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

func validateAnalyseFeatures(req AnalyseRequest) error {
	if err := validateAnalyseFormatFeatures(req); err != nil {
		return err
	}
	return validateAnalyseVulnerabilityFeatures(req)
}

func validateAnalyseFormatFeatures(req AnalyseRequest) error {
	if req.Format != report.FormatCycloneDX {
		return nil
	}
	if req.Features.Enabled(report.SBOMAttestationExportsPreviewFeature) {
		return nil
	}
	return fmt.Errorf("analyse format %q requires --enable-feature %s", report.FormatCycloneDX, report.SBOMAttestationExportsPreviewFeature)
}

func validateAnalyseVulnerabilityFeatures(req AnalyseRequest) error {
	if !analyseVulnerabilityFeatureRequested(req) {
		return nil
	}
	if req.Features.Enabled(report.ReachabilityVulnerabilityPrioritizationPreviewFeature) {
		return nil
	}
	return fmt.Errorf("reachable vulnerability prioritization requires --enable-feature %s", report.ReachabilityVulnerabilityPrioritizationPreviewFeature)
}

func analyseVulnerabilityFeatureRequested(req AnalyseRequest) bool {
	if strings.TrimSpace(req.AdvisorySourcePath) != "" {
		return true
	}
	threshold := report.NormalizeVulnerabilityPriorityThreshold(req.Thresholds.ReachableVulnerabilityPriority)
	return threshold != "" && threshold != report.VulnerabilityPriorityOff
}
