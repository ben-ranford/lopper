//go:build !windows

package analysis

func validateExplicitCachePath(string) error {
	return nil
}

func validateRawUserCacheDir(string) error {
	return nil
}
