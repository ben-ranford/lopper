//go:build !windows

package safeio

func rejectUnsupportedWindowsRoot(string) error {
	return nil
}

func rejectUnsupportedWindowsRelativePath(string) error {
	return nil
}

func rejectUnsupportedWindowsPath(string, string) error {
	return nil
}
