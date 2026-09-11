package cpp

import "fmt"

const (
	maxScannableCPPFile     int64 = 2 * 1024 * 1024
	maxCompileDatabaseBytes int64 = 8 * 1024 * 1024
)

func oversizedCPPInputWarning(path string, maxBytes int64) string {
	return fmt.Sprintf("skipped %s larger than %d bytes", path, maxBytes)
}
