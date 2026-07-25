//go:build windows

package safeio

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/windowspath"
)

func rejectUnsupportedWindowsRoot(name string) error {
	pathInfo := windowspath.Classify(name)
	switch pathInfo.Kind {
	case windowspath.KindDriveRelative:
		return fmt.Errorf("root must not be drive-relative on Windows: %s", name)
	case windowspath.KindRootedWithoutDrive:
		return fmt.Errorf("root must include a drive or UNC share on Windows: %s", name)
	case windowspath.KindAmbiguous:
		return fmt.Errorf("root must not use Windows device or namespace forms: %s", name)
	case windowspath.KindUNCIncomplete:
		return fmt.Errorf("root must include a UNC host and share on Windows: %s", name)
	}
	if windowspath.HasTrimmedComponentAlias(name) {
		return fmt.Errorf("root must not contain Windows trailing dot or space aliases: %s", name)
	}
	if windowspath.HasReservedDOSNameComponent(name) {
		return fmt.Errorf("root must not contain reserved DOS device names on Windows: %s", name)
	}
	return nil
}
