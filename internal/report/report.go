package report

import (
	"errors"
	"fmt"
	"strings"
)

type Format string

const (
	FormatTable     Format = "table"
	FormatCSV       Format = "csv"
	FormatJSON      Format = "json"
	FormatSARIF     Format = "sarif"
	FormatPRComment Format = "pr-comment"
	FormatCycloneDX Format = "cyclonedx-json"
	FormatSPDX      Format = "spdx-json"
	FormatVEX       Format = "cyclonedx-vex-json"
)

const SBOMAttestationExportsPreviewFeature = "sbom-attestation-exports-preview"
const ReachabilityVulnerabilityPrioritizationPreviewFeature = "reachability-vulnerability-prioritization-preview"
const DependencyIdentityPreviewFeature = "dependency-identity-preview"
const AdvisoryOSVSyncPreviewFeature = "advisory-osv-sync-preview"
const VulnerabilityExceptionsVEXPreviewFeature = "vulnerability-exceptions-vex-preview"
const SPDXSBOMExportPreviewFeature = "spdx-sbom-export-preview"
const DependencySurfacePRReviewPreviewFeature = "dependency-surface-pr-review-preview"

var ErrUnknownFormat = errors.New("unknown format")

func ParseFormat(value string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(FormatTable):
		return FormatTable, nil
	case string(FormatCSV):
		return FormatCSV, nil
	case string(FormatJSON):
		return FormatJSON, nil
	case string(FormatSARIF):
		return FormatSARIF, nil
	case string(FormatPRComment):
		return FormatPRComment, nil
	case string(FormatCycloneDX):
		return FormatCycloneDX, nil
	case string(FormatSPDX):
		return FormatSPDX, nil
	case string(FormatVEX):
		return FormatVEX, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownFormat, value)
	}
}
