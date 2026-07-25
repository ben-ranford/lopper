//go:build !windows

package safeio

func rejectUnsupportedWindowsRoot(string) error {
	return nil
}
