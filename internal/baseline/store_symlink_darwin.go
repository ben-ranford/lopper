//go:build darwin

package baseline

import (
	"os"
	"path/filepath"
)

func isAllowedSystemRootSymlink(path string) bool {
	expectedTarget, ok := map[string]string{
		"/etc": "/private/etc",
		"/tmp": "/private/tmp",
		"/var": "/private/var",
	}[filepath.Clean(path)]
	if !ok {
		return false
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return filepath.Clean(target) == expectedTarget
}
