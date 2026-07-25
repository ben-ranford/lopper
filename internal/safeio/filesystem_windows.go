//go:build windows

package safeio

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/windowspath"
)

func rejectUnsupportedWindowsRoot(name string) error {
	return rejectUnsupportedWindowsPath("root", name)
}

func rejectUnsupportedWindowsRelativePath(name string) error {
	return rejectUnsupportedWindowsPath("path", name)
}

func rejectUnsupportedWindowsPath(kind, name string) error {
	pathInfo := windowspath.Classify(name)
	switch pathInfo.Kind {
	case windowspath.KindDriveRelative:
		return fmt.Errorf("%s must not be drive-relative on Windows: %s", kind, name)
	case windowspath.KindRootedWithoutDrive:
		return fmt.Errorf("%s must include a drive or UNC share on Windows: %s", kind, name)
	case windowspath.KindAmbiguous:
		return fmt.Errorf("%s must not use Windows device or namespace forms: %s", kind, name)
	case windowspath.KindUNCIncomplete:
		return fmt.Errorf("%s must include a UNC host and share on Windows: %s", kind, name)
	}
	if windowspath.HasTrimmedComponentAlias(name) {
		return fmt.Errorf("%s must not contain Windows trailing dot or space aliases: %s", kind, name)
	}
	if windowspath.HasReservedDOSNameComponent(name) {
		return fmt.Errorf("%s must not contain reserved DOS device names on Windows: %s", kind, name)
	}
	return nil
}
