package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
)

type Formatter struct{}

func NewFormatter() Formatter {
	return Formatter{}
}

func (f Formatter) Format(report Report, format Format) (string, error) {
	switch format {
	case FormatTable:
		return formatTable(report), nil
	case FormatJSON:
		payload, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return "", err
		}
		return string(payload) + "\n", nil
	default:
		return "", ErrUnknownFormat
	}
}

func formatTable(report Report) string {
	if len(report.Dependencies) == 0 {
		return formatEmpty(report)
	}

	var buffer bytes.Buffer
	if report.Summary != nil {
		fmt.Fprintf(
			&buffer,
			"Summary: %d deps, Used/Total: %d/%d (%.1f%%)\n\n",
			report.Summary.DependencyCount,
			report.Summary.UsedExportsCount,
			report.Summary.TotalExportsCount,
			report.Summary.UsedPercent,
		)
	}
	writer := tabwriter.NewWriter(&buffer, 0, 0, 2, ' ', 0)

	fmt.Fprintln(writer, "Dependency\tUsed/Total\tUsed%\tEst. Unused Size\tTop Symbols")
	for _, dep := range report.Dependencies {
		usedPercent := dep.UsedPercent
		if usedPercent <= 0 && dep.TotalExportsCount > 0 {
			usedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
		}
		usedTotal := fmt.Sprintf("%d/%d", dep.UsedExportsCount, dep.TotalExportsCount)
		fmt.Fprintf(
			writer,
			"%s\t%s\t%.1f\t%s\t%s\n",
			dep.Name,
			usedTotal,
			usedPercent,
			formatBytes(dep.EstimatedUnusedBytes),
			formatTopSymbols(dep.TopUsedSymbols),
		)
	}

	writer.Flush()
	appendWarnings(&buffer, report)

	return buffer.String()
}

func formatEmpty(report Report) string {
	var buffer bytes.Buffer
	buffer.WriteString("No dependencies to report.\n")
	appendWarnings(&buffer, report)
	return buffer.String()
}

func appendWarnings(buffer *bytes.Buffer, report Report) {
	if len(report.Warnings) == 0 {
		return
	}
	buffer.WriteString("\nWarnings:\n")
	for _, warning := range report.Warnings {
		buffer.WriteString("- ")
		buffer.WriteString(warning)
		buffer.WriteString("\n")
	}
}

func formatTopSymbols(symbols []SymbolUsage) string {
	if len(symbols) == 0 {
		return "-"
	}

	items := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol.Count > 1 {
			items = append(items, fmt.Sprintf("%s (%d)", symbol.Name, symbol.Count))
		} else {
			items = append(items, symbol.Name)
		}
	}
	return strings.Join(items, ", ")
}

func formatBytes(value int64) string {
	if value == 0 {
		return "0 B"
	}

	abs := value
	if abs < 0 {
		abs = -abs
	}
	units := []string{"B", "KB", "MB", "GB"}
	unitIndex := 0
	floatValue := float64(abs)
	for floatValue >= 1024 && unitIndex < len(units)-1 {
		floatValue /= 1024
		unitIndex++
	}

	formatted := fmt.Sprintf("%.1f %s", floatValue, units[unitIndex])
	if value < 0 {
		return "-" + formatted
	}
	return formatted
}
