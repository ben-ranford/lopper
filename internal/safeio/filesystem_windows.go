//go:build windows

package safeio

import (
	"github.com/ben-ranford/lopper/internal/windowspath"
)

func rejectUnsupportedWindowsRoot(name string) error {
	return rejectUnsupportedWindowsPath("root", name)
}

func rejectUnsupportedWindowsRelativePath(name string) error {
	return rejectUnsupportedWindowsPath("path", name)
}

func rejectUnsupportedWindowsPath(kind, name string) error {
	return windowspath.ValidateUnsupported(name, windowspath.UnsupportedPathErrorMessages{
		DriveRelative:      kind + " must not be drive-relative on Windows: %s",
		RootedWithoutDrive: kind + " must include a drive or UNC share on Windows: %s",
		Ambiguous:          kind + " must not use Windows device or namespace forms: %s",
		UNCIncomplete:      kind + " must include a UNC host and share on Windows: %s",
		TrimmedAlias:       kind + " must not contain Windows trailing dot or space aliases: %s",
		ReservedDOSName:    kind + " must not contain reserved DOS device names on Windows: %s",
	})
}
