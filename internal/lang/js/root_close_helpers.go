package js

import (
	"io"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func closeRootAppendWarning(root safeio.Root, warnings *[]string, warning string) bool {
	if root.Close() != nil {
		*warnings = append(*warnings, warning)
		return true
	}
	return false
}

func closeRootResetResolution(root safeio.Root, resolved *string, ok *bool) {
	if root.Close() != nil {
		*resolved = ""
		*ok = false
	}
}

func closeRootResetSlice(root safeio.Root, values *[]string) {
	if root.Close() != nil {
		*values = nil
	}
}

func closeRootResetLicense(root safeio.Root, license **report.DependencyLicense) {
	if root.Close() != nil {
		*license = nil
	}
}

func closeRootResetProbe(root safeio.Root, probe **licenseFileProbe) {
	if root.Close() != nil {
		*probe = nil
	}
}

func closeReadCloserPreserveErr(closer io.Closer, err *error) {
	if closeErr := closer.Close(); closeErr != nil && *err == nil {
		*err = closeErr
	}
}
