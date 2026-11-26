package report

import (
	"encoding/json"
)

type Formatter struct{}

func NewFormatter() *Formatter {
	return &Formatter{}
}

func (f *Formatter) Format(report Report, format Format) (string, error) {
	FinalizeDerivedState(&report)

	switch format {
	case FormatTable:
		return formatTable(report)
	case FormatCSV:
		return formatCSV(report)
	case FormatJSON:
		return formatJSON(report)
	case FormatSARIF:
		return formatSARIF(report)
	case FormatPRComment:
		return formatPRComment(report), nil
	case FormatCycloneDX:
		return formatCycloneDXJSON(report)
	case FormatSPDX:
		return formatSPDXJSON(report)
	case FormatVEX:
		return formatCycloneDXVEXJSON(report)
	default:
		return "", ErrUnknownFormat
	}
}

// FormatTableView formats a table with caller-provided derived aggregates.
// It is for views that intentionally display a dependency page with summary
// data computed from a larger filtered set.
func (f *Formatter) FormatTableView(report Report) (string, error) {
	return formatTable(report)
}

func formatJSON(report Report) (string, error) {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload) + "\n", nil
}
