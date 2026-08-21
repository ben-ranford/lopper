package cpp

import "fmt"

const (
	maxScannableCPPFile     int64 = 2 * 1024 * 1024
	maxCPPManifestBytes     int64 = 2 * 1024 * 1024
	maxCPPLockBytes         int64 = 8 * 1024 * 1024
	maxCompileDatabaseBytes int64 = 8 * 1024 * 1024
)

func cppDependencyManifestByteLimit(filename string) (int64, bool) {
	switch filename {
	case vcpkgManifestFile, conanManifestFile:
		return maxCPPManifestBytes, true
	case vcpkgLockFile, conanLockFile:
		return maxCPPLockBytes, true
	default:
		return 0, false
	}
}

func oversizedCPPInputWarning(path string, maxBytes int64) string {
	return fmt.Sprintf("skipped %s larger than %d bytes", path, maxBytes)
}
