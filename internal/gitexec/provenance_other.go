//go:build !unix && !windows

package gitexec

import "fmt"

const platformSafeSystemPath = "PATH="

func platformExecutableCandidates() []string {
	return nil
}

func validatePlatformExecutable(string) error {
	return fmt.Errorf("git executable provenance is unsupported on this platform")
}
