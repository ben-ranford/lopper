package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dashboardPathWithinRoot applies the same volume-aware containment rule to
// dashboard output roots and remote checkout roots.
func dashboardPathWithinRoot(rootAbs, targetAbs string) (bool, error) {
	if dashboardPathsUseDifferentWindowsVolumes(rootAbs, targetAbs) {
		return false, nil
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return false, fmt.Errorf("compute dashboard path: %w", err)
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))), nil
}

func dashboardPathWithinDir(rootAbs, targetAbs string) bool {
	within, err := dashboardPathWithinRoot(rootAbs, targetAbs)
	return err == nil && within
}

func dashboardPathsUseDifferentWindowsVolumes(rootAbs, targetAbs string) bool {
	rootVolume := dashboardPathVolumeName(rootAbs)
	targetVolume := dashboardPathVolumeName(targetAbs)
	return rootVolume != "" && targetVolume != "" && rootVolume != targetVolume
}

func dashboardPathVolumeName(path string) string {
	volume := filepath.VolumeName(path)
	if volume == "" && len(path) >= 2 && path[1] == ':' {
		drive := path[0]
		if ('a' <= drive && drive <= 'z') || ('A' <= drive && drive <= 'Z') {
			volume = path[:2]
		}
	}
	return strings.ToLower(volume)
}
