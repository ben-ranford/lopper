package report

import "github.com/ben-ranford/lopper/internal/terminal"

func sanitizeTerminalString(value string) string {
	return terminal.SanitizeString(value)
}

func sanitizeTerminalStrings(values []string) []string {
	return terminal.SanitizeStrings(values)
}
