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
)

const SBOMAttestationExportsPreviewFeature = "sbom-attestation-exports-preview"

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
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownFormat, value)
	}
}
