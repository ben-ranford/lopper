//go:build !darwin

package baseline

func isAllowedSystemRootSymlink(string) bool {
	return false
}
