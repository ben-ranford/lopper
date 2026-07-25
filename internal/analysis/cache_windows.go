//go:build windows

package analysis

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/windowspath"
)

func validateExplicitCachePath(path string) error {
	pathInfo := windowspath.Classify(path)
	switch pathInfo.Kind {
	case windowspath.KindDriveRelative:
		return fmt.Errorf("resolve cache root: explicit Windows cache path must not be drive-relative: %s", path)
	case windowspath.KindRootedWithoutDrive:
		return fmt.Errorf("resolve cache root: explicit Windows cache path must include a drive or UNC share: %s", path)
	case windowspath.KindAmbiguous:
		return fmt.Errorf("resolve cache root: explicit Windows cache path must not use device or namespace forms: %s", path)
	case windowspath.KindUNCIncomplete:
		return fmt.Errorf("resolve cache root: explicit Windows cache path must include a UNC host and share: %s", path)
	}
	if windowspath.HasTrimmedComponentAlias(path) {
		return fmt.Errorf("resolve cache root: explicit Windows cache path must not contain trailing dot or space aliases: %s", path)
	}
	if windowspath.HasReservedDOSNameComponent(path) {
		return fmt.Errorf("resolve cache root: explicit Windows cache path must not contain reserved DOS device names: %s", path)
	}
	return nil
}
