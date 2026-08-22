package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

var (
	writeProfileConfigCanonicalIfAbsentFn  = safeio.WriteFileAtomicallyIfAbsentUnderCanonicalPath
	writeProfileConfigCanonicalReplacingFn = safeio.WriteFileAtomicallyReplacingUnderCanonicalPath
)

type profileConfigCanonicalWriter func(string, []byte, os.FileMode) error

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

func persistProfileConfigForced(config, outputPath string) error {
	return persistProfileConfigThroughDestination(outputPath, []byte(config), func(destination commandOutputDestination, data []byte) error {
		return destination.root.WriteFileCreatingParentsWithPermissionFallback(destination.targetPath, data, 0o600, 0o750)
	}, writeProfileConfigCanonicalReplacingFn)
}

func persistProfileConfigIfAbsent(config, outputPath string) error {
	return persistProfileConfigThroughDestination(outputPath, []byte(config), func(destination commandOutputDestination, data []byte) error {
		return destination.root.WriteFileCreatingParentsIfAbsent(destination.targetPath, data, 0o600, 0o750)
	}, writeProfileConfigCanonicalIfAbsentFn)
}

func persistProfileConfigThroughDestination(outputPath string, data []byte, write func(commandOutputDestination, []byte) error, writeCanonical profileConfigCanonicalWriter) (returnErr error) {
	destination, err := openCommandOutputDestination(outputPath)
	if err != nil {
		if profilePermissionError(err) {
			return persistProfileConfigCanonical(data, outputPath, "", err, writeCanonical)
		}
		return err
	}
	defer func() {
		if closeErr := destination.root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()

	if err := write(destination, data); err != nil {
		if profilePermissionError(err) {
			return persistProfileConfigCanonicalThroughDestination(data, destination, err, writeCanonical)
		}
		return err
	}
	return returnErr
}

func persistProfileConfigCanonicalThroughDestination(data []byte, destination commandOutputDestination, primaryErr error, write profileConfigCanonicalWriter) error {
	if err := safeio.VerifyDirectoryIdentity(destination.rootAbs, destination.rootInfo); err != nil {
		return errors.Join(primaryErr, err)
	}
	return write(filepath.Join(destination.rootAbs, destination.targetPath), data, 0o600)
}

func persistProfileConfigCanonical(data []byte, outputPath, outputAbs string, primaryErr error, write profileConfigCanonicalWriter) error {
	if outputAbs == "" {
		resolvedOutputPath, resolveErr := absoluteCommandOutputPath(outputPath)
		if resolveErr != nil {
			return errors.Join(primaryErr, resolveErr)
		}
		outputAbs = resolvedOutputPath
	}
	return write(outputAbs, data, 0o600)
}

func profilePermissionError(err error) bool {
	return errors.Is(err, os.ErrPermission) || os.IsPermission(err)
}
