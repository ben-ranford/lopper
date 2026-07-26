package windowspath

import "fmt"

type UnsupportedPathErrorMessages struct {
	DriveRelative      string
	RootedWithoutDrive string
	Ambiguous          string
	UNCIncomplete      string
	TrimmedAlias       string
	ReservedDOSName    string
}

func ValidateUnsupported(value string, messages UnsupportedPathErrorMessages) error {
	pathInfo := Classify(value)
	switch pathInfo.Kind {
	case KindDriveRelative:
		return unsupportedPathError(messages.DriveRelative, value)
	case KindRootedWithoutDrive:
		return unsupportedPathError(messages.RootedWithoutDrive, value)
	case KindAmbiguous:
		return unsupportedPathError(messages.Ambiguous, value)
	case KindUNCIncomplete:
		return unsupportedPathError(messages.UNCIncomplete, value)
	}
	if HasTrimmedComponentAlias(value) {
		return unsupportedPathError(messages.TrimmedAlias, value)
	}
	if HasReservedDOSNameComponent(value) {
		return unsupportedPathError(messages.ReservedDOSName, value)
	}
	return nil
}

func unsupportedPathError(format, value string) error {
	if format == "" {
		return nil
	}
	return fmt.Errorf(format, value)
}
