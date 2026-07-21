package app

import (
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func validateAnalyseFeatures(req AnalyseRequest) error {
	if err := validateAnalyseFormatFeatures(req); err != nil {
		return err
	}
	return validateAnalysisPolicyFeatures(req.Features, req.AdvisorySourcePath, req.Thresholds, req.VulnerabilityExceptions)
}

func validateAnalyseFormatFeatures(req AnalyseRequest) error {
	switch req.Format {
	case report.FormatCycloneDX:
		if req.Features.Enabled(report.SBOMAttestationExportsPreviewFeature) {
			return nil
		}
		return fmt.Errorf("analyse format %q requires --enable-feature %s", report.FormatCycloneDX, report.SBOMAttestationExportsPreviewFeature)
	case report.FormatSPDX:
		if req.Features.Enabled(report.SPDXSBOMExportPreviewFeature) {
			return nil
		}
		return fmt.Errorf("analyse format %q requires --enable-feature %s", report.FormatSPDX, report.SPDXSBOMExportPreviewFeature)
	case report.FormatVEX:
		if req.Features.Enabled(report.VulnerabilityExceptionsVEXPreviewFeature) {
			return nil
		}
		return fmt.Errorf("analyse format %q requires --enable-feature %s", report.FormatVEX, report.VulnerabilityExceptionsVEXPreviewFeature)
	default:
		return nil
	}
}

func validateAnalysisPolicyFeatures(features featureflags.Set, advisorySourcePath string, values thresholds.Values, vulnerabilityExceptions []report.VulnerabilityException) error {
	if err := validateAnalysisExceptionFeatures(features, vulnerabilityExceptions); err != nil {
		return err
	}
	return validateAnalysisVulnerabilityFeatures(features, advisorySourcePath, values)
}

func validateAnalysisVulnerabilityFeatures(features featureflags.Set, advisorySourcePath string, values thresholds.Values) error {
	if !analysisVulnerabilityFeatureRequested(advisorySourcePath, values) {
		return nil
	}
	if features.Enabled(report.ReachabilityVulnerabilityPrioritizationPreviewFeature) {
		return nil
	}
	return fmt.Errorf("reachable vulnerability prioritization requires --enable-feature %s", report.ReachabilityVulnerabilityPrioritizationPreviewFeature)
}

func validateAnalysisExceptionFeatures(features featureflags.Set, vulnerabilityExceptions []report.VulnerabilityException) error {
	if len(vulnerabilityExceptions) == 0 {
		return nil
	}
	if features.Enabled(report.VulnerabilityExceptionsVEXPreviewFeature) {
		return nil
	}
	return fmt.Errorf("vulnerability exceptions require --enable-feature %s", report.VulnerabilityExceptionsVEXPreviewFeature)
}

func analysisVulnerabilityFeatureRequested(advisorySourcePath string, values thresholds.Values) bool {
	if strings.TrimSpace(advisorySourcePath) != "" {
		return true
	}
	threshold := report.NormalizeVulnerabilityPriorityThreshold(values.ReachableVulnerabilityPriority)
	return threshold != "" && threshold != report.VulnerabilityPriorityOff
}
