//go:build !windows

package runtime

import "path/filepath"

func platformRuntimeExecutableStageRoot(sourcePath string) (string, error) {
	return filepath.Dir(sourcePath), nil
}
