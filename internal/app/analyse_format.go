package app

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/report"
)

func validateAnalyseFormatFeatures(req AnalyseRequest) error {
	if req.Format != report.FormatCycloneDX {
		return nil
	}
	if req.Features.Enabled(report.SBOMAttestationExportsPreviewFeature) {
		return nil
	}
	return fmt.Errorf("analyse format %q requires --enable-feature %s", report.FormatCycloneDX, report.SBOMAttestationExportsPreviewFeature)
}
