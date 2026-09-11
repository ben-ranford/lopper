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
	writeProfileConfigPinnedRootIfAbsentFn  = (*safeio.WriteRoot).WriteFileAtomicallyIfAbsentUnderPinnedRoot
	writeProfileConfigPinnedRootReplacingFn = (*safeio.WriteRoot).WriteFileAtomicallyReplacingUnderPinnedRoot
	openProfileSearchOnlyWriteRootFn        = safeio.OpenCanonicalSearchOnlyWriteRoot
)

type profileConfigPinnedRootWriter func(*safeio.WriteRoot, string, []byte, os.FileMode) error

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
	}, writeProfileConfigPinnedRootReplacingFn)
}

func persistProfileConfigIfAbsent(config, outputPath string) error {
	return persistProfileConfigThroughDestination(outputPath, []byte(config), func(destination commandOutputDestination, data []byte) error {
		return destination.root.WriteFileCreatingParentsIfAbsent(destination.targetPath, data, 0o600, 0o750)
	}, writeProfileConfigPinnedRootIfAbsentFn)
}

func persistProfileConfigThroughDestination(outputPath string, data []byte, write func(commandOutputDestination, []byte) error, writePinnedRoot profileConfigPinnedRootWriter) (returnErr error) {
	resolvedDestination, err := resolveCommandOutputDestination(outputPath)
	if err != nil {
		return err
	}
	destination, err := openResolvedCommandOutputDestination(resolvedDestination, openCommandOutputWriteRootFn)
	if err != nil {
		if profilePermissionError(err) {
			return persistProfileConfigSearchOnlyThroughResolvedDestination(resolvedDestination, data, err, writePinnedRoot)
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
			return persistProfileConfigPinnedRootFallback(data, destination, err, writePinnedRoot)
		}
		return err
	}
	return returnErr
}

func persistProfileConfigSearchOnlyThroughResolvedDestination(destination commandOutputDestination, data []byte, primaryErr error, write profileConfigPinnedRootWriter) (returnErr error) {
	destination, err := openResolvedCommandOutputDestination(destination, openProfileSearchOnlyWriteRootFn)
	if err != nil {
		if errors.Is(err, safeio.ErrSearchOnlyWriteRootUnsupported) {
			return primaryErr
		}
		return errors.Join(primaryErr, err)
	}
	defer func() {
		if closeErr := destination.root.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, closeErr)
		}
	}()
	return persistProfileConfigPinnedRootFallback(data, destination, primaryErr, write)
}

func persistProfileConfigPinnedRootFallback(data []byte, destination commandOutputDestination, primaryErr error, write profileConfigPinnedRootWriter) error {
	if err := destination.root.VerifyIdentity(destination.rootInfo); err != nil {
		return errors.Join(primaryErr, err)
	}
	if err := write(destination.root, destination.targetPath, data, 0o600); err != nil {
		if errors.Is(err, safeio.ErrSearchOnlyWriteRootUnsupported) {
			return primaryErr
		}
		return err
	}
	return nil
}

func profilePermissionError(err error) bool {
	return errors.Is(err, os.ErrPermission) || os.IsPermission(err)
}
