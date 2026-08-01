package app

import (
	"errors"
	"fmt"
	"os"
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

func persistProfileConfig(config, outputPath string, force bool) (result string, returnErr error) {
	trimmedOutputPath := strings.TrimSpace(outputPath)
	if trimmedOutputPath == "" || trimmedOutputPath == "-" {
		return config, nil
	}
	if hasDirectoryStyleOutputPath(trimmedOutputPath) {
		return "", fmt.Errorf("output path must name a file: %s", trimmedOutputPath)
	}
	destination, err := openCommandOutputDestination(trimmedOutputPath)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := destination.root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	if !force {
		if err := destination.root.WriteFileCreatingParentsIfAbsent(destination.targetPath, []byte(config), 0o600, 0o750); err != nil {
			if errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("%s already exists; pass --force to overwrite", trimmedOutputPath)
			}
			return "", err
		}
	} else if err := writeCommandOutputFileFn(destination.root, destination.targetPath, []byte(config), 0o600, 0o750); err != nil {
		return "", err
	}
	return "threshold profile config written to " + trimmedOutputPath, nil
}
