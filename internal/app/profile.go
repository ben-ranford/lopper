package app

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

var (
	writeProfileConfigCanonicalIfAbsentFn  = safeio.WriteFileAtomicallyIfAbsentUnderCanonicalPath
	writeProfileConfigCanonicalReplacingFn = safeio.WriteFileAtomicallyReplacingUnderCanonicalPath
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
	if !force {
		if err := persistProfileConfigIfAbsent(config, trimmedOutputPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("%s already exists; pass --force to overwrite", trimmedOutputPath)
			}
			return "", err
		}
		return "threshold profile config written to " + trimmedOutputPath, nil
	}

	if err := persistProfileConfigForced(config, trimmedOutputPath); err != nil {
		return "", err
	}
	return "threshold profile config written to " + trimmedOutputPath, nil
}

func persistProfileConfigForced(config, outputPath string) (returnErr error) {
	destination, err := openCommandOutputDestination(outputPath)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return persistProfileConfigCanonicalReplacing(config, outputPath, err)
		}
		return err
	}
	defer func() {
		if closeErr := destination.root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	if err := destination.root.WriteFileCreatingParentsWithPermissionFallback(destination.targetPath, []byte(config), 0o600, 0o750); err != nil {
		return err
	}
	return returnErr
}

func persistProfileConfigIfAbsent(config, outputPath string) error {
	destination, err := openCommandOutputDestination(outputPath)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return persistProfileConfigCanonicalIfAbsent(config, outputPath, err)
		}
		return err
	}
	returnErr := destination.root.WriteFileCreatingParentsIfAbsent(destination.targetPath, []byte(config), 0o600, 0o750)
	if closeErr := destination.root.Close(); closeErr != nil {
		returnErr = errors.Join(returnErr, closeErr)
	}
	return returnErr
}

func persistProfileConfigCanonicalIfAbsent(config, outputPath string, primaryErr error) error {
	resolvedOutputPath, resolveErr := absoluteCommandOutputPath(outputPath)
	if resolveErr != nil {
		return errors.Join(primaryErr, resolveErr)
	}
	return writeProfileConfigCanonicalIfAbsentFn(resolvedOutputPath, []byte(config), 0o600)
}

func persistProfileConfigCanonicalReplacing(config, outputPath string, primaryErr error) error {
	resolvedOutputPath, resolveErr := absoluteCommandOutputPath(outputPath)
	if resolveErr != nil {
		return errors.Join(primaryErr, resolveErr)
	}
	return writeProfileConfigCanonicalReplacingFn(resolvedOutputPath, []byte(config), 0o600)
}
