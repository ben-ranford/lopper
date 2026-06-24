package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/thresholds"
)

func (a *App) executeProfile(req Request) (string, error) {
	if !req.Profile.Features.Enabled(thresholds.ProfilesPreviewFeature) {
		return "", ErrProfileFeatureDisabled
	}
	config, err := thresholds.ProfileConfigYAML(req.Profile.Name)
	if err != nil {
		return "", err
	}
	return persistProfileConfig(config, req.Profile.OutputPath, req.Profile.Force)
}

func persistProfileConfig(config, outputPath string, force bool) (string, error) {
	trimmedOutputPath := strings.TrimSpace(outputPath)
	if trimmedOutputPath == "" || trimmedOutputPath == "-" {
		return config, nil
	}
	if !force {
		if _, err := os.Stat(trimmedOutputPath); err == nil {
			return "", fmt.Errorf("%s already exists; pass --force to overwrite", trimmedOutputPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(trimmedOutputPath), 0o750); err != nil {
		return "", err
	}
	if err := os.WriteFile(trimmedOutputPath, []byte(config), 0o600); err != nil {
		return "", err
	}
	return "threshold profile config written to " + trimmedOutputPath, nil
}
