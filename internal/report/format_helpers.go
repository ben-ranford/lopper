package report

import (
	"fmt"
)

func formatBytes(value int64) string {
	if value == 0 {
		return "0 B"
	}

	scaled := float64(value)
	if scaled < 0 {
		scaled = -scaled
	}

	unit := "B"
	if scaled >= 1024 {
		scaled /= 1024
		unit = "KB"
		if scaled >= 1024 {
			scaled /= 1024
			unit = "MB"
			if scaled >= 1024 {
				scaled /= 1024
				unit = "GB"
			}
		}
	}

	formatted := fmt.Sprintf("%.1f %s", scaled, unit)
	if value < 0 {
		return "-" + formatted
	}
	return formatted
}
