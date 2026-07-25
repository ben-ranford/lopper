//go:build !windows

package safeio

func windowsReplaceExistingRenameFallback(error, string, string) bool {
	return false
}
