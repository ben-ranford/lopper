//go:build windows

package analysis

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/windowspath"
)

func validateExplicitCachePath(path string) error {
	return validateWindowsCachePath("resolve cache root: explicit Windows cache path", path)
}

func validateRawUserCacheDir(path string) error {
	return validateWindowsCachePath("user cache dir", path)
}

func validateWindowsCachePath(kind, path string) error {
	pathInfo := windowspath.Classify(path)
	switch pathInfo.Kind {
	case windowspath.KindDriveRelative:
		return fmt.Errorf("%s must not be drive-relative: %s", kind, path)
	case windowspath.KindRootedWithoutDrive:
		return fmt.Errorf("%s must include a drive or UNC share: %s", kind, path)
	case windowspath.KindAmbiguous:
		return fmt.Errorf("%s must not use device or namespace forms: %s", kind, path)
	case windowspath.KindUNCIncomplete:
		return fmt.Errorf("%s must include a UNC host and share: %s", kind, path)
	}
	if windowspath.HasTrimmedComponentAlias(path) {
		return fmt.Errorf("%s must not contain trailing dot or space aliases: %s", kind, path)
	}
	if windowspath.HasReservedDOSNameComponent(path) {
		return fmt.Errorf("%s must not contain reserved DOS device names: %s", kind, path)
	}
	return nil
}
