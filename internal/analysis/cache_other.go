//go:build !windows

package analysis

func validateExplicitCachePath(string) error {
	return nil
}
