//go:build windows

package analysis

import (
	"github.com/ben-ranford/lopper/internal/windowspath"
)

func validateExplicitCachePath(path string) error {
	return validateWindowsCachePath("resolve cache root: explicit Windows cache path", path)
}

func validateRawUserCacheDir(path string) error {
	return validateWindowsCachePath("user cache dir", path)
}

func validateWindowsCachePath(kind, path string) error {
	return windowspath.ValidateUnsupported(path, windowspath.UnsupportedPathErrorMessages{
		DriveRelative:      kind + " must not be drive-relative: %s",
		RootedWithoutDrive: kind + " must include a drive or UNC share: %s",
		Ambiguous:          kind + " must not use device or namespace forms: %s",
		UNCIncomplete:      kind + " must include a UNC host and share: %s",
		TrimmedAlias:       kind + " must not contain trailing dot or space aliases: %s",
		ReservedDOSName:    kind + " must not contain reserved DOS device names: %s",
	})
}
