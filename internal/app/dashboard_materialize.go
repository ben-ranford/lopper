package app

import (
	"os"
	"path/filepath"
	"strings"
)

func persistDashboardOutput(formatted, outputPath string) (string, error) {
	return persistCommandOutput(formatted, outputPath, "dashboard report")
}

func persistCommandOutput(formatted, outputPath, label string) (string, error) {
	trimmedOutputPath := strings.TrimSpace(outputPath)
	if trimmedOutputPath == "" || trimmedOutputPath == "-" {
		return formatted, nil
	}

	if err := os.MkdirAll(filepath.Dir(trimmedOutputPath), 0o750); err != nil {
		return "", err
	}
	if err := os.WriteFile(trimmedOutputPath, []byte(formatted), 0o600); err != nil {
		return "", err
	}
	return label + " written to " + trimmedOutputPath, nil
}
