package report

import (
	"encoding/json"
)

type Formatter struct{}

func NewFormatter() *Formatter {
	return &Formatter{}
}

func (f *Formatter) Format(report Report, format Format) (string, error) {
	switch format {
	case FormatTable:
		return formatTable(report)
	case FormatJSON:
		return formatJSON(report)
	case FormatSARIF:
		return formatSARIF(report)
	case FormatPRComment:
		return formatPRComment(report), nil
	default:
		return "", ErrUnknownFormat
	}
}

func formatJSON(report Report) (string, error) {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", err
	}
	return string(payload) + "\n", nil
}
