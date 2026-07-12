//go:build darwin

package baseline

import "testing"

func TestAllowsVerifiedDarwinSystemRootSymlinks(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/etc", "/tmp", "/var"} {
		if !isAllowedSystemRootSymlink(path) {
			t.Errorf("expected verified Darwin system alias %s to be allowed", path)
		}
	}
}
