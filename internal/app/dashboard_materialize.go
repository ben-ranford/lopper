package app

import (
	"os"
	"path/filepath"
	"strings"
)

func persistDashboardOutput(formatted, outputPath string) (string, error) {
	trimmedOutputPath := strings.TrimSpace(outputPath)
	if trimmedOutputPath == "" {
		return formatted, nil
	}

	if err := os.MkdirAll(filepath.Dir(trimmedOutputPath), 0o750); err != nil {
		return "", err
	}
	if err := os.WriteFile(trimmedOutputPath, []byte(formatted), 0o600); err != nil {
		return "", err
	}
	return "dashboard report written to " + trimmedOutputPath, nil
}
